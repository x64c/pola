package security

import "encoding/binary"

// CipherContext states WHERE an at-rest value lives. It is sealed into the
// ciphertext as associated data — authenticated, not encrypted, not stored —
// and must be re-supplied, identically, to decrypt. A value moved anywhere
// else (another row, another field, another cookie) stops decrypting.
//
// The context is a required parameter, not an option: an optional one would
// be forgotten at exactly the call sites that need it. Location must be
// non-empty; App, Field and Host qualify it where the location name alone is
// not unique (cookie names are package constants shared across apps, and
// across the hosts one app serves; hash rows hold many fields).
type CipherContext struct {
	App      string // owning app, when Location isn't already app-scoped; empty otherwise
	Location string // where the value lives: KVDB row key, cookie name — required
	Field    string // sub-location: hash field name; empty when Location alone identifies it
	Host     string // owning web host's name, for a cookie of an app that serves several hosts; empty otherwise
}

// render is the canonical byte form sealed as associated data: each part
// length-prefixed (uvarint), in field order. Length prefixes keep distinct
// contexts distinct — ("ab","c") never renders like ("a","bc") — and the
// cipher owning the rendering keeps callers from composing ad-hoc strings
// that drift between seal and open sites.
//
// Host is rendered only when set, after the other three. A context without
// one therefore renders exactly as it did before Host existed — every value
// sealed then still opens — and a context with one can never render like a
// context without: the hostless form is a strict prefix of it.
func (cc CipherContext) render() []byte {
	buf := make([]byte, 0, len(cc.App)+len(cc.Location)+len(cc.Field)+len(cc.Host)+8)
	for _, part := range [...]string{cc.App, cc.Location, cc.Field} {
		buf = binary.AppendUvarint(buf, uint64(len(part)))
		buf = append(buf, part...)
	}
	if cc.Host != "" {
		buf = binary.AppendUvarint(buf, uint64(len(cc.Host)))
		buf = append(buf, cc.Host...)
	}
	return buf
}
