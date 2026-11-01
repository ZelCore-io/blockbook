package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file reproduces the JavaScript behavior the electrum-http proxy's
// answers depend on: its query parsing (decodeURIComponent, qs and the two
// backward-compatible URL forms), JSON.stringify's number and string format,
// and bitgo-utxo-lib's script.toASM.

// ehValue is a query value as the proxy sees it: undefined (!set), a string,
// or an array/object qs built from repeated or bracketed keys (multi, with
// the string values joined by "," as JavaScript's toString does).
type ehValue struct {
	set   bool
	multi bool
	s     string
}

// truthy is JavaScript truthiness: undefined and "" are falsy, arrays are not.
func (v ehValue) truthy() bool { return v.set && (v.multi || v.s != "") }

type electrumHTTPQuery struct {
	call, param, server ehValue
}

// jsSplitAt returns element i of JavaScript's s.split(sep), undefined when
// there is none.
func jsSplitAt(s, sep string, i int) ehValue {
	parts := strings.Split(s, sep)
	if i < len(parts) {
		return ehValue{set: true, s: parts[i]}
	}
	return ehValue{}
}

// jsDecodeURIComponent decodes %XX escapes and fails, like JavaScript, on a
// malformed escape or an escape sequence that is not UTF-8. '+' is kept.
func jsDecodeURIComponent(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		// Collect the run of escapes so multi-byte UTF-8 is validated whole.
		var run []byte
		for i < len(s) && s[i] == '%' {
			if i+3 > len(s) {
				return "", errors.New("URI malformed")
			}
			v, err := hex.DecodeString(s[i+1 : i+3])
			if err != nil {
				return "", errors.New("URI malformed")
			}
			run = append(run, v[0])
			i += 3
		}
		i--
		if !utf8.Valid(run) {
			return "", errors.New("URI malformed")
		}
		b.Write(run)
	}
	return b.String(), nil
}

// qsDecode is qs's default decoder: '+' becomes a space, then
// decodeURIComponent; a malformed value is kept as it is.
func qsDecode(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	if d, err := jsDecodeURIComponent(s); err == nil {
		return d
	}
	return s
}

var qsBracket = regexp.MustCompile(`\[[^[\]]*\]`)

// qsParse parses a query string like qs.parse with default options, for the
// top-level keys. A bracketed key (a[b]=c) or a repeated key yields a multi
// value under its root key.
func qsParse(q ehValue) map[string]ehValue {
	out := map[string]ehValue{}
	if !q.set {
		return out
	}
	for _, part := range strings.Split(q.s, "&") {
		if part == "" {
			continue
		}
		pos := strings.Index(part, "]=")
		if pos == -1 {
			pos = strings.Index(part, "=")
		} else {
			pos++
		}
		var key, val string
		if pos == -1 {
			key = qsDecode(part)
		} else {
			key = qsDecode(part[:pos])
			val = qsDecode(part[pos+1:])
		}
		if key == "" {
			continue
		}
		multi := false
		if loc := qsBracket.FindStringIndex(key); loc != nil {
			if loc[0] == 0 {
				// qs uses a leading bracket group as the root ("[a]=x" is a=x).
				multi = loc[1] != len(key)
				key = key[1 : loc[1]-1]
			} else {
				key = key[:loc[0]]
				multi = true
			}
		}
		if prev, ok := out[key]; ok {
			out[key] = ehValue{set: true, multi: true, s: prev.s + "," + val}
			continue
		}
		out[key] = ehValue{set: true, multi: multi, s: val}
	}
	return out
}

// parseElectrumHTTPQuery extracts call, param and server from the request
// target exactly as index.js does, including its two backward-compatible URL
// forms that work on the raw, undecoded target. The error is the proxy's
// decodeURIComponent failure.
func parseElectrumHTTPQuery(rawURL string) (electrumHTTPQuery, error) {
	dec, err := jsDecodeURIComponent(rawURL)
	if err != nil {
		return electrumHTTPQuery{}, err
	}
	parsed := qsParse(jsSplitAt(dec, "/?", 1))
	q := electrumHTTPQuery{call: parsed["call"], param: parsed["param"], server: parsed["server"]}
	// index.js: "support backwards compatibility" (/?server=X?call=Y?param=Z).
	if !q.call.truthy() || !q.server.truthy() {
		x := strings.Split(rawURL, "?param=")
		q.param = jsSplitAt(rawURL, "?param=", 1)
		q.call = jsSplitAt(x[0], "?call=", 1)
		y := strings.Split(x[0], "?call=")
		q.server = jsSplitAt(y[0], "?server=", 1)
	}
	// index.js: "support mobile mistake where it was ?param".
	if (q.call.multi || q.call.s != "height") && q.call.set && !q.param.set {
		x := strings.Split(rawURL, "?param=")
		q.param = jsSplitAt(rawURL, "?param=", 1)
		q.call = jsSplitAt(x[0], "&call=", 1)
	}
	return q, nil
}

// jsParam renders a value as JavaScript string interpolation does
// (undefined -> "undefined").
func (v ehValue) jsParam() string {
	if !v.set {
		return "undefined"
	}
	return v.s
}

// pyParam renders a value as ElectrumX formats an argument the proxy sent
// (undefined becomes JSON null, Python's None).
func (v ehValue) pyParam() string {
	if !v.set {
		return "None"
	}
	return v.s
}

// ehObject is a JSON object that keeps its key order, as JavaScript objects
// do for string keys.
type ehObject []ehField

type ehField struct {
	key   string
	value interface{}
}

func (o ehObject) get(key string) (interface{}, bool) {
	for _, f := range o {
		if f.key == key {
			return f.value, true
		}
	}
	return nil, false
}

// set replaces a key in place or appends it, like a JavaScript assignment.
func (o *ehObject) set(key string, value interface{}) {
	for i := range *o {
		if (*o)[i].key == key {
			(*o)[i].value = value
			return
		}
	}
	*o = append(*o, ehField{key, value})
}

// ehDecode decodes JSON into ehObject, []interface{}, float64, string, bool
// and nil: numbers become doubles as in JavaScript.
func ehDecode(data []byte) (interface{}, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := ehDecodeValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

func ehDecodeValue(d *json.Decoder) (interface{}, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := ehObject{}
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("invalid object key")
				}
				v, err := ehDecodeValue(d)
				if err != nil {
					return nil, err
				}
				o.set(k, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			a := []interface{}{}
			for d.More() {
				v, err := ehDecodeValue(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return a, nil
		}
		return nil, errors.New("unexpected delimiter")
	case json.Number:
		return strconv.ParseFloat(string(t), 64)
	default:
		return t, nil // string, bool or nil
	}
}

func ehMarshal(v interface{}) ([]byte, error) {
	var b bytes.Buffer
	if err := ehWrite(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ehWrite writes v as JSON.stringify would.
func ehWrite(b *bytes.Buffer, v interface{}) error {
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case string:
		jsString(b, v)
	case float64:
		jsNumber(b, v)
	case int:
		b.WriteString(strconv.Itoa(v))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(v), 10))
	case ehObject:
		b.WriteByte('{')
		for i, f := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			jsString(b, f.key)
			b.WriteByte(':')
			if err := ehWrite(b, f.value); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []interface{}:
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := ehWrite(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []ehObject:
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := ehWrite(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		return fmt.Errorf("electrum-http: cannot encode %T", v)
	}
	return nil
}

// jsNumber formats a double as JavaScript's Number#toString: shortest
// round-trip digits, exponent form below 1e-6 and from 1e21, and "0" for -0.
// NaN and the infinities become null, as in JSON.stringify.
func jsNumber(b *bytes.Buffer, f float64) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		b.WriteString("null")
		return
	}
	if f == 0 {
		b.WriteByte('0')
		return
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs < 1e-6 || abs >= 1e21 {
		format = 'e'
	}
	s := strconv.AppendFloat(nil, f, format, -1, 64)
	if format == 'e' {
		// Go writes e-07 / e+21; JavaScript writes e-7 / e+21.
		if n := len(s); n >= 4 && s[n-4] == 'e' && s[n-2] == '0' {
			s[n-2] = s[n-1]
			s = s[:n-1]
		}
	}
	b.Write(s)
}

// jsString writes a JSON string as JSON.stringify does: only '"', '\\' and
// control characters are escaped.
func jsString(b *bytes.Buffer, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[r>>4])
				b.WriteByte(hexDigits[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// bitgoOpNames maps opcodes to bitcoin-ops names as bitgo-utxo-lib's
// REVERSE_OPS has them (the last alias wins: OP_0, OP_1,
// OP_CHECKLOCKTIMEVERIFY, OP_CHECKSEQUENCEVERIFY).
var bitgoOpNames = func() map[byte]string {
	m := map[byte]string{
		0: "OP_0", 76: "OP_PUSHDATA1", 77: "OP_PUSHDATA2", 78: "OP_PUSHDATA4", 79: "OP_1NEGATE", 80: "OP_RESERVED",
		97: "OP_NOP", 98: "OP_VER", 99: "OP_IF", 100: "OP_NOTIF", 101: "OP_VERIF", 102: "OP_VERNOTIF", 103: "OP_ELSE",
		104: "OP_ENDIF", 105: "OP_VERIFY", 106: "OP_RETURN", 107: "OP_TOALTSTACK", 108: "OP_FROMALTSTACK",
		109: "OP_2DROP", 110: "OP_2DUP", 111: "OP_3DUP", 112: "OP_2OVER", 113: "OP_2ROT", 114: "OP_2SWAP",
		115: "OP_IFDUP", 116: "OP_DEPTH", 117: "OP_DROP", 118: "OP_DUP", 119: "OP_NIP", 120: "OP_OVER",
		121: "OP_PICK", 122: "OP_ROLL", 123: "OP_ROT", 124: "OP_SWAP", 125: "OP_TUCK", 126: "OP_CAT",
		127: "OP_SUBSTR", 128: "OP_LEFT", 129: "OP_RIGHT", 130: "OP_SIZE", 131: "OP_INVERT", 132: "OP_AND",
		133: "OP_OR", 134: "OP_XOR", 135: "OP_EQUAL", 136: "OP_EQUALVERIFY", 137: "OP_RESERVED1",
		138: "OP_RESERVED2", 139: "OP_1ADD", 140: "OP_1SUB", 141: "OP_2MUL", 142: "OP_2DIV", 143: "OP_NEGATE",
		144: "OP_ABS", 145: "OP_NOT", 146: "OP_0NOTEQUAL", 147: "OP_ADD", 148: "OP_SUB", 149: "OP_MUL",
		150: "OP_DIV", 151: "OP_MOD", 152: "OP_LSHIFT", 153: "OP_RSHIFT", 154: "OP_BOOLAND", 155: "OP_BOOLOR",
		156: "OP_NUMEQUAL", 157: "OP_NUMEQUALVERIFY", 158: "OP_NUMNOTEQUAL", 159: "OP_LESSTHAN",
		160: "OP_GREATERTHAN", 161: "OP_LESSTHANOREQUAL", 162: "OP_GREATERTHANOREQUAL", 163: "OP_MIN",
		164: "OP_MAX", 165: "OP_WITHIN", 166: "OP_RIPEMD160", 167: "OP_SHA1", 168: "OP_SHA256",
		169: "OP_HASH160", 170: "OP_HASH256", 171: "OP_CODESEPARATOR", 172: "OP_CHECKSIG",
		173: "OP_CHECKSIGVERIFY", 174: "OP_CHECKMULTISIG", 175: "OP_CHECKMULTISIGVERIFY", 176: "OP_NOP1",
		177: "OP_CHECKLOCKTIMEVERIFY", 178: "OP_CHECKSEQUENCEVERIFY", 253: "OP_PUBKEYHASH", 254: "OP_PUBKEY",
		255: "OP_INVALIDOPCODE",
	}
	for i := byte(1); i <= 16; i++ {
		m[80+i] = "OP_" + strconv.Itoa(int(i))
	}
	for i := byte(4); i <= 10; i++ {
		m[175+i] = "OP_NOP" + strconv.Itoa(int(i))
	}
	return m
}()

// bitgoMinimalOP is bitcoinjs's asMinimalOP: the opcode a minimal push of
// data would be, or -1.
func bitgoMinimalOP(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	if len(data) != 1 {
		return -1
	}
	if data[0] >= 1 && data[0] <= 16 {
		return 80 + int(data[0])
	}
	if data[0] == 0x81 {
		return 79
	}
	return -1
}

// bitgoASM is bitgo-utxo-lib's script.toASM(buffer): pushes as hex (or the
// small-number opcode a minimal push would be), opcodes by name, an unknown
// opcode as an empty token, and "" for a script whose push runs past its end.
func bitgoASM(script []byte) string {
	var tokens []string
	for i := 0; i < len(script); {
		op := script[i]
		if op > 0 && op <= 78 {
			var n, size int
			switch {
			case op < 76:
				n, size = int(op), 1
			case op == 76:
				if i+2 > len(script) {
					return ""
				}
				n, size = int(script[i+1]), 2
			case op == 77:
				if i+3 > len(script) {
					return ""
				}
				n, size = int(script[i+1])|int(script[i+2])<<8, 3
			default:
				if i+5 > len(script) {
					return ""
				}
				n = int(uint32(script[i+1]) | uint32(script[i+2])<<8 | uint32(script[i+3])<<16 | uint32(script[i+4])<<24)
				size = 5
			}
			i += size
			if n < 0 || i+n > len(script) {
				return ""
			}
			data := script[i : i+n]
			i += n
			if m := bitgoMinimalOP(data); m >= 0 {
				tokens = append(tokens, bitgoOpNames[byte(m)])
			} else {
				tokens = append(tokens, hex.EncodeToString(data))
			}
			continue
		}
		tokens = append(tokens, bitgoOpNames[op])
		i++
	}
	return strings.Join(tokens, " ")
}

// pyRepr formats a string as Python's repr does for ASCII text, for the
// daemon errors ElectrumX passes through.
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteByte(quote)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyThousands groups a decimal digit string as Python's "{:,d}".
func pyThousands(digits string) string {
	var b strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
