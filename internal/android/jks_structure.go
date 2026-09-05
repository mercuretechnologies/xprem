package android

import (
	"crypto/sha1"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
)

// validJKSStructure walks the JKS v1/v2 framing without allocating from file
// lengths. keystore-go allocates before reading keys, certificates and chains;
// all of those lengths must be bounded by the actual input before calling it.
// Password verification and decoding remain the keystore library's job.
func validJKSStructure(data []byte) bool {
	if len(data) < 12+sha1.Size {
		return false
	}
	s := cryptobyte.String(data[:len(data)-sha1.Size])
	var magic, version, entries uint32
	if !s.ReadUint32(&magic) || magic != jksMagic || !s.ReadUint32(&version) ||
		(version != 1 && version != 2) || !s.ReadUint32(&entries) {
		return false
	}
	// Each entry needs a tag, an alias length and a timestamp at minimum.
	if uint64(entries) > uint64(len(s)/14) {
		return false
	}
	for range entries {
		var tag, certificates uint32
		var alias cryptobyte.String
		if !s.ReadUint32(&tag) || !s.ReadUint16LengthPrefixed(&alias) || !s.Skip(8) {
			return false
		}
		switch tag {
		case 1: // Private key, then its certificate chain.
			var key cryptobyte.String
			if !readJKSBlob(&s, &key) || !validJKSKeyEnvelope(key) || !s.ReadUint32(&certificates) {
				return false
			}
		case 2: // Trusted certificate.
			certificates = 1
		default:
			return false
		}
		// Even a zero-length v1 certificate consumes a uint32 length.
		if uint64(certificates) > uint64(len(s)/4) {
			return false
		}
		for range certificates {
			var certificateType cryptobyte.String
			if version == 2 && !s.ReadUint16LengthPrefixed(&certificateType) {
				return false
			}
			var certificate cryptobyte.String
			if !readJKSBlob(&s, &certificate) {
				return false
			}
		}
	}
	return s.Empty()
}

func readJKSBlob(s *cryptobyte.String, out *cryptobyte.String) bool {
	var length uint32
	if !s.ReadUint32(&length) || uint64(length) > uint64(len(*s)) {
		return false
	}
	*out = (*s)[:int(length)]
	return s.Skip(int(length))
}

func validJKSKeyEnvelope(key cryptobyte.String) bool {
	var sequence, algorithm, encrypted cryptobyte.String
	// The library subtracts the 20-byte salt and SHA-1 checksum before
	// allocating its decrypted buffer. A shorter envelope would panic.
	return key.ReadASN1(&sequence, asn1.SEQUENCE) && key.Empty() &&
		sequence.ReadASN1(&algorithm, asn1.SEQUENCE) &&
		sequence.ReadASN1(&encrypted, asn1.OCTET_STRING) && sequence.Empty() &&
		len(encrypted) >= 2*sha1.Size
}
