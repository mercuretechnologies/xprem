package ios

import (
	"bytes"
	"encoding/asn1"
	"errors"
)

const (
	tagOctetString = 0x04
	tagOID         = 0x06
	tagSequence    = 0x30
	tagSet         = 0x31
	tagExplicit0   = 0xa0
	constructedBit = 0x20
	maxBERDepth    = 32
)

var (
	oidSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidData       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	errMalformed  = errors.New("malformed CMS structure")
)

// berElement is one BER TLV. tag keeps the class and constructed bits of the
// identifier octet; content excludes an indefinite length's end-of-contents.
type berElement struct {
	tag     byte
	content []byte
}

// constructed reports whether the BER tag contains child elements.
func (e berElement) constructed() bool {
	return e.tag&constructedBit != 0
}

// signedDataContent extracts the encapsulated content of a CMS SignedData
// blob, accepting the indefinite BER lengths that encoding/asn1 rejects.
func signedDataContent(data []byte) ([]byte, error) {
	root, rest, err := readBER(data, 0)
	if err != nil || len(rest) != 0 {
		return nil, errMalformed
	}
	if err := validateBER(root, 0); err != nil {
		return nil, err
	}
	contentInfo, err := children(root, tagSequence, 2)
	if err != nil || !isOID(contentInfo[0], oidSignedData) {
		return nil, errMalformed
	}
	explicitSignedData, err := children(contentInfo[1], tagExplicit0, 1)
	if err != nil {
		return nil, err
	}
	signedData, err := children(explicitSignedData[0], tagSequence, 3)
	if err != nil {
		return nil, err
	}
	encapsulated, err := children(signedData[2], tagSequence, 2)
	if err != nil || !isOID(encapsulated[0], oidData) {
		return nil, errMalformed
	}
	explicitContent, err := children(encapsulated[1], tagExplicit0, 1)
	if err != nil {
		return nil, err
	}
	return octets(explicitContent[0])
}

// validateBER bounds nesting throughout the envelope before handing it to the signature parser.
func validateBER(element berElement, depth int) error {
	if depth > maxBERDepth {
		return errMalformed
	}
	if !element.constructed() {
		return nil
	}
	for rest := element.content; len(rest) > 0; {
		child, next, err := readBER(rest, depth+1)
		if err != nil {
			return err
		}
		if err := validateBER(child, depth+1); err != nil {
			return err
		}
		rest = next
	}
	return nil
}

// readBER reads one bounded BER element, supporting definite and indefinite lengths.
func readBER(data []byte, depth int) (berElement, []byte, error) {
	if depth > maxBERDepth || len(data) < 2 || data[0]&0x1f == 0x1f {
		return berElement{}, nil, errMalformed
	}
	element := berElement{tag: data[0]}
	lengthOctet := data[1]
	data = data[2:]
	if lengthOctet == 0x80 {
		if !element.constructed() {
			return berElement{}, nil, errMalformed
		}
		rest := data
		for !(len(rest) >= 2 && rest[0] == 0 && rest[1] == 0) {
			_, next, err := readBER(rest, depth+1)
			if err != nil {
				return berElement{}, nil, err
			}
			rest = next
		}
		element.content = data[:len(data)-len(rest)]
		return element, rest[2:], nil
	}
	length := int(lengthOctet)
	if lengthOctet > 0x80 {
		size := int(lengthOctet & 0x7f)
		if size > 4 || len(data) < size {
			return berElement{}, nil, errMalformed
		}
		length = 0
		for _, b := range data[:size] {
			length = length<<8 | int(b)
		}
		data = data[size:]
	}
	if length < 0 || length > len(data) {
		return berElement{}, nil, errMalformed
	}
	element.content = data[:length]
	return element, data[length:], nil
}

// children decodes the direct children of a constructed element with the
// given tag, requiring at least min of them.
func children(element berElement, tag byte, min int) ([]berElement, error) {
	if element.tag != tag {
		return nil, errMalformed
	}
	var decoded []berElement
	for rest := element.content; len(rest) > 0; {
		child, next, err := readBER(rest, 0)
		if err != nil {
			return nil, err
		}
		decoded = append(decoded, child)
		rest = next
	}
	if len(decoded) < min {
		return nil, errMalformed
	}
	return decoded, nil
}

// octets reads a primitive OCTET STRING or a constructed one made of
// primitive segments.
func octets(element berElement) ([]byte, error) {
	if element.tag == tagOctetString {
		return element.content, nil
	}
	segments, err := children(element, tagOctetString|constructedBit, 1)
	if err != nil {
		return nil, err
	}
	var content bytes.Buffer
	for _, segment := range segments {
		if segment.tag != tagOctetString {
			return nil, errMalformed
		}
		content.Write(segment.content)
	}
	return content.Bytes(), nil
}

// isOID compares a BER object identifier with the expected ASN.1 identifier.
func isOID(element berElement, expected asn1.ObjectIdentifier) bool {
	if element.tag != tagOID {
		return false
	}
	var oid asn1.ObjectIdentifier
	rest, err := asn1.Unmarshal(append([]byte{tagOID, byte(len(element.content))}, element.content...), &oid)
	return err == nil && len(rest) == 0 && oid.Equal(expected)
}
