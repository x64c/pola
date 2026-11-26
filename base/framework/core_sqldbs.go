package framework

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"

	"github.com/x64c/pola/base/sqldbs"
)

// PrepareSQLDBClients builds the app's SQL clients and broadcasts its raw SQL
// sets to them. Each preparer is one driver's: it is given the app root and
// the client map, and adds the clients its own conf describes. Then every
// set in c.RawSQLFSMap (set it before this call) is loaded into EVERY client
// under the set's name, so all clients carry the same store names and a
// handler can stay dialect-blind (see sqldbs's raw store doc). The databases
// themselves are prepared afterwards, by PrepareSQLDatabases.
func (c *Core) PrepareSQLDBClients(preparers ...func(string, map[string]sqldbs.Client) error) error {
	c.SQLDBClients = make(map[string]sqldbs.Client)
	for _, fn := range preparers {
		if err := fn(c.AppRoot, c.SQLDBClients); err != nil {
			return err
		}
	}
	for fsName, sqlFS := range c.RawSQLFSMap {
		for clientName, client := range c.SQLDBClients {
			if err := client.LoadRawSQL(fsName, sqlFS); err != nil {
				return fmt.Errorf("sqldb client %q: load %q: %w", clientName, fsName, err)
			}
		}
	}
	return nil
}

func (c *Core) PrepareSQLDatabases() error {
	confFilePath := filepath.Join(c.AppRoot, "config", ".sqldbs.json")
	confBytes, err := os.ReadFile(confFilePath)
	if err != nil {
		return fmt.Errorf("sqldbs: %w", err)
	}
	var clientDBsConfMap map[string]map[string]jsontext.Value
	if err = json.Unmarshal(confBytes, &clientDBsConfMap); err != nil {
		return fmt.Errorf("sqldbs: %w", err)
	}
	for clientName, dbsConfMap := range clientDBsConfMap {
		client, ok := c.SQLDBClients[clientName]
		if !ok {
			return fmt.Errorf("sqldbs[%s]: unknown client", clientName)
		}
		for dbName, dbRawConf := range dbsConfMap {
			if err = client.PrepareDB(dbName, dbRawConf); err != nil {
				return fmt.Errorf("sqldbs[%s][%s]: %w", clientName, dbName, err)
			}
		}
	}
	return nil
}
