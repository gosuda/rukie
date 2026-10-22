package ci

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

type Statement struct {
	Supersedes *string `json:"supersedes,omitempty"`
	Evidence   *string `json:"evidence,omitempty"`
	Subject    string  `json:"subject"`
	Kind       string  `json:"kind"`
	Result     string  `json:"result"`
	SignedAt   string  `json:"signed_at"`
}

const (
	attestDomainV1  = "ruk-attest-payload-v1"
	attestDomainV2  = "ruk-attest-payload-v2"
	attestNamespace = "ruk-attest"
)

func sshString(out *bytes.Buffer, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	out.Write(length[:])
	out.Write(data)
}

func subjectFormat(subject string) ([]byte, string, error) {
	raw, err := hex.DecodeString(subject)
	if err != nil {
		return nil, "", fmt.Errorf("CanonicalPayload: %w", err)
	}
	switch len(raw) {
	case 20:
		return raw, "sha1", nil
	case 32:
		return raw, "sha256", nil
	default:
		return nil, "", fmt.Errorf("CanonicalPayload: subject has %d bytes, want 20 or 32", len(raw))
	}
}
func optionalDigest(value *string, field string) ([]byte, error) {
	if value == nil {
		return []byte{}, nil
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(*value, "sha256:"))
	if err != nil {
		return nil, fmt.Errorf("CanonicalPayload: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("CanonicalPayload: %s digest has %d bytes, want 32", field, len(raw))
	}
	return raw, nil
}
func payloadV1(statement Statement) ([][]byte, error) {
	if statement.Evidence != nil {
		return nil, fmt.Errorf("CanonicalPayloadV1: a statement carrying evidence has no v1 payload")
	}
	subject, format, err := subjectFormat(statement.Subject)
	if err != nil {
		return nil, err
	}
	superseded, err := optionalDigest(statement.Supersedes, "supersedes")
	if err != nil {
		return nil, err
	}
	return [][]byte{
		[]byte(attestDomainV1), []byte(format), subject,
		[]byte(statement.Kind), []byte(statement.Result),
		[]byte(statement.SignedAt), superseded,
	}, nil
}
func payloadV2(statement Statement) ([][]byte, error) {
	subject, format, err := subjectFormat(statement.Subject)
	if err != nil {
		return nil, err
	}
	superseded, err := optionalDigest(statement.Supersedes, "supersedes")
	if err != nil {
		return nil, err
	}
	evidence, err := optionalDigest(statement.Evidence, "evidence")
	if err != nil {
		return nil, err
	}
	return [][]byte{
		[]byte(attestDomainV2), []byte(format), subject,
		[]byte(statement.Kind), []byte(statement.Result),
		[]byte(statement.SignedAt), superseded, evidence,
	}, nil
}

func encodeFields(fields [][]byte) []byte {
	var out bytes.Buffer
	for _, field := range fields {
		sshString(&out, field)
	}
	return out.Bytes()
}

func CanonicalPayloadV1(statement Statement) ([]byte, error) {
	if statement.Evidence != nil {
		return nil, fmt.Errorf("CanonicalPayloadV1: a statement carrying evidence has no v1 payload")
	}
	fields, err := payloadV1(statement)
	if err != nil {
		return nil, err
	}
	if len(fields) != 7 {
		return nil, fmt.Errorf("CanonicalPayloadV1: %d fields, want 7", len(fields))
	}
	return encodeFields(fields), nil
}

func CanonicalPayloadV2(statement Statement) ([]byte, error) {
	fields, err := payloadV2(statement)
	if err != nil {
		return nil, err
	}
	if len(fields) != 8 {
		return nil, fmt.Errorf("CanonicalPayloadV2: %d fields, want 8", len(fields))
	}
	return encodeFields(fields), nil
}

func CanonicalPayload(statement Statement) ([]byte, error) {
	if statement.Evidence != nil {
		return CanonicalPayloadV2(statement)
	}
	return CanonicalPayloadV1(statement)
}

func SplitPayload(payload []byte) ([][]byte, error) {
	var fields [][]byte
	rest := payload
	for len(rest) > 0 {
		if len(rest) < 4 {
			return nil, fmt.Errorf("SplitPayload: truncated length prefix")
		}
		length := binary.BigEndian.Uint32(rest[:4])
		rest = rest[4:]
		if uint64(length) > uint64(len(rest)) {
			return nil, fmt.Errorf("SplitPayload: field length %d exceeds %d remaining bytes", length, len(rest))
		}
		fields = append(fields, rest[:length])
		rest = rest[length:]
	}
	return fields, nil
}

func SignSSHSIG(payload []byte, key ed25519.PrivateKey) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("SignSSHSIG: private key has %d bytes, want %d", len(key), ed25519.PrivateKeySize)
	}
	sum := sha256.Sum256(payload)
	var signed bytes.Buffer
	signed.WriteString("SSHSIG")
	sshString(&signed, []byte(attestNamespace))
	sshString(&signed, nil)
	sshString(&signed, []byte("sha256"))
	sshString(&signed, sum[:])
	signature := ed25519.Sign(key, signed.Bytes())
	public := key.Public().(ed25519.PublicKey)
	var wireSignature bytes.Buffer
	sshString(&wireSignature, []byte("ssh-ed25519"))
	sshString(&wireSignature, signature)
	var blob bytes.Buffer
	blob.WriteString("SSHSIG")
	var version [4]byte
	binary.BigEndian.PutUint32(version[:], 1)
	blob.Write(version[:])
	sshString(&blob, publicKeyWire(public))
	sshString(&blob, []byte(attestNamespace))
	sshString(&blob, nil)
	sshString(&blob, []byte("sha256"))
	sshString(&blob, wireSignature.Bytes())
	encoded := base64.StdEncoding.EncodeToString(blob.Bytes())
	var armor strings.Builder
	armor.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(encoded) > 0 {
		width := min(70, len(encoded))
		armor.WriteString(encoded[:width])
		armor.WriteByte('\n')
		encoded = encoded[width:]
	}
	armor.WriteString("-----END SSH SIGNATURE-----\n")
	return armor.String(), nil
}

func publicKeyWire(public ed25519.PublicKey) []byte {
	var out bytes.Buffer
	sshString(&out, []byte("ssh-ed25519"))
	sshString(&out, public)
	return out.Bytes()
}

func VerifySSHSIG(payload []byte, armor string) (ed25519.PublicKey, error) {
	body := strings.TrimPrefix(armor, "-----BEGIN SSH SIGNATURE-----\n")
	body = strings.TrimSuffix(body, "-----END SSH SIGNATURE-----\n")
	compact := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, body)
	raw, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return nil, fmt.Errorf("VerifySSHSIG: %w", err)
	}
	rest := raw
	consume := func() ([]byte, error) {
		if len(rest) < 4 {
			return nil, fmt.Errorf("VerifySSHSIG: truncated SSHSIG")
		}
		length := binary.BigEndian.Uint32(rest[:4])
		rest = rest[4:]
		if uint64(length) > uint64(len(rest)) {
			return nil, fmt.Errorf("VerifySSHSIG: SSHSIG field overruns")
		}
		field := rest[:length]
		rest = rest[length:]
		return field, nil
	}
	if len(rest) < len("SSHSIG") || string(rest[:len("SSHSIG")]) != "SSHSIG" {
		return nil, fmt.Errorf("VerifySSHSIG: magic is %q, want SSHSIG", rest)
	}
	rest = rest[len("SSHSIG"):]
	if len(rest) < 4 {
		return nil, fmt.Errorf("VerifySSHSIG: truncated SSHSIG version")
	}
	if binary.BigEndian.Uint32(rest[:4]) != 1 {
		return nil, fmt.Errorf("VerifySSHSIG: unsupported version")
	}
	rest = rest[4:]
	keyWire, err := consume()
	if err != nil {
		return nil, err
	}
	namespace, err := consume()
	if err != nil {
		return nil, err
	}
	if string(namespace) != attestNamespace {
		return nil, fmt.Errorf("VerifySSHSIG: namespace is %q, want %q", namespace, attestNamespace)
	}
	reserved, err := consume()
	if err != nil {
		return nil, err
	}
	if len(reserved) != 0 {
		return nil, fmt.Errorf("VerifySSHSIG: reserved bytes must be empty")
	}
	hashName, err := consume()
	if err != nil {
		return nil, err
	}
	if string(hashName) != "sha256" {
		return nil, fmt.Errorf("VerifySSHSIG: hash is %q, want sha256", hashName)
	}
	signatureWire, err := consume()
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("VerifySSHSIG: %d trailing bytes", len(rest))
	}
	keyRest := keyWire
	take := func(source []byte) ([]byte, []byte, error) {
		if len(source) < 4 {
			return nil, nil, fmt.Errorf("VerifySSHSIG: truncated key field")
		}
		length := binary.BigEndian.Uint32(source[:4])
		source = source[4:]
		if uint64(length) > uint64(len(source)) {
			return nil, nil, fmt.Errorf("VerifySSHSIG: key field overruns")
		}
		return source[:length], source[length:], nil
	}
	keyName, keyRest, err := take(keyRest)
	if err != nil {
		return nil, err
	}
	if string(keyName) != "ssh-ed25519" {
		return nil, fmt.Errorf("VerifySSHSIG: key algorithm is %q, want ssh-ed25519", keyName)
	}
	keyBytes, keyRest, err := take(keyRest)
	if err != nil {
		return nil, err
	}
	if len(keyRest) != 0 || len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("VerifySSHSIG: public key has %d bytes, want %d", len(keyBytes), ed25519.PublicKeySize)
	}
	sigRest := signatureWire
	sigName, sigRest, err := take(sigRest)
	if err != nil {
		return nil, err
	}
	if string(sigName) != "ssh-ed25519" {
		return nil, fmt.Errorf("VerifySSHSIG: signature algorithm is %q, want ssh-ed25519", sigName)
	}
	sigBytes, sigRest, err := take(sigRest)
	if err != nil {
		return nil, err
	}
	if len(sigRest) != 0 || len(sigBytes) != ed25519.SignatureSize {
		return nil, fmt.Errorf("VerifySSHSIG: signature has %d bytes, want %d", len(sigBytes), ed25519.SignatureSize)
	}
	sum := sha256.Sum256(payload)
	var signed bytes.Buffer
	signed.WriteString("SSHSIG")
	sshString(&signed, []byte(attestNamespace))
	sshString(&signed, nil)
	sshString(&signed, []byte("sha256"))
	sshString(&signed, sum[:])
	if !ed25519.Verify(ed25519.PublicKey(keyBytes), signed.Bytes(), sigBytes) {
		return nil, fmt.Errorf("VerifySSHSIG: signature does not match the payload")
	}
	return ed25519.PublicKey(keyBytes), nil
}
