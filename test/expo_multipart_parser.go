package test

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"xprem/config"
	"xprem/internal/keyStore"
)

// This is a reimplementation of the @expo/multipart-body-parser[https://www.npmjs.com/package/@expo/multipart-body-parser] in Go to test manifest response

type MultipartPart struct {
	Body        string
	Headers     map[string]string
	Name        string
	Disposition string
	Parameters  map[string]string
}

func ParseMultipartMixedResponse(contentTypeHeader string, bodyBuffer []byte) ([]MultipartPart, error) {
	mediaType, params, err := mime.ParseMediaType(contentTypeHeader)
	if err != nil {
		return nil, err
	}

	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil, err
	}

	boundary, ok := params["boundary"]
	if !ok {
		return nil, err
	}

	reader := multipart.NewReader(bytes.NewReader(bodyBuffer), boundary)
	var parts []MultipartPart

	for {
		part, err := reader.NextPart()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}

		body, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}

		headers := make(map[string]string)
		for key, values := range part.Header {
			headers[key] = strings.Join(values, ", ")
		}

		disposition, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))

		parts = append(parts, MultipartPart{
			Body:        string(body),
			Headers:     headers,
			Name:        params["name"],
			Disposition: disposition,
			Parameters:  params,
		})
	}

	return parts, nil
}

func IsMultipartPartWithName(part MultipartPart, name string) bool {
	return part.Name == name
}

func ValidateSignatureHeader(appId string, signature string, content string) bool {
	app, err := config.GetAppConfig(appId)
	if err != nil {
		fmt.Println("Unknown app id:", appId)
		return false
	}
	publicCert := keyStore.GetPublicExpoKey(*app)
	signatureParts := strings.Split(signature, ",")
	if len(signatureParts) != 2 {
		fmt.Println("Invalid signature format")
		return false
	}
	signatureParts[0] = strings.TrimPrefix(signatureParts[0], "sig=")
	signatureParts[1] = strings.TrimPrefix(signatureParts[1], " keyid=")
	signatureParts[1] = strings.Trim(signatureParts[1], "\"")
	signatureParts[0] = strings.Trim(signatureParts[0], "\"")
	if signatureParts[1] != "main" {
		fmt.Println("Invalid keyid")
		return false
	}
	decodedSignature, err := base64.StdEncoding.DecodeString(signatureParts[0])
	if err != nil {
		fmt.Println("Error decoding signature: ", err)
		return false
	}

	block, _ := pem.Decode([]byte(publicCert))
	if block == nil {
		fmt.Println("Failed to parse public certificate")
		return false
	}

	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		fmt.Println("Error parsing public key: ", err)
		return false
	}

	rsaPublicKey, ok := publicKey.(*rsa.PublicKey)
	if !ok {
		fmt.Println("Public key is not of type RSA")
		return false
	}

	hash := sha256.New()
	hash.Write([]byte(content))
	hashedData := hash.Sum(nil)

	err = rsa.VerifyPKCS1v15(rsaPublicKey, crypto.SHA256, hashedData, decodedSignature)
	if err != nil {
		fmt.Println("Signature verification failed: ", err)
		return false
	}

	return true
}
