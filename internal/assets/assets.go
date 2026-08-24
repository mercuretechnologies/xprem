package assets

import (
	"errors"
	"log"
	"net/http"
	"xprem/internal/bucket"
	"xprem/internal/cdn"
	"xprem/internal/types"
	"xprem/internal/update"
)

type AssetsRequest struct {
	AppId          string
	Branch         string
	AssetName      string
	RuntimeVersion string
	Platform       types.Platform
	RequestID      string
	Update         *types.Update
}

type AssetsResponse struct {
	StatusCode  int
	Headers     map[string]string
	Body        []byte
	ContentType string
	URL         string
}

// validatedAsset is the outcome of the shared request validation: the update
// the asset is served from and its manifest entry.
type validatedAsset struct {
	update        *types.Update
	isLaunchAsset bool
	assetMetadata types.Asset
}

// validateAssetRequest runs the checks common to both delivery paths and must
// stay their single entry point: the URL path hands req.AssetName to the CDN
// layer, which builds (and for CloudFront signs) whatever key it is given,
// and the file path would otherwise proxy internal objects like
// update-metadata.json living next to real assets. A non-nil response means
// the request must be refused as-is.
func validateAssetRequest(req AssetsRequest) (validatedAsset, *AssetsResponse) {
	requestID := req.RequestID

	if req.AssetName == "" {
		log.Printf("[RequestID: %s] No asset name provided", requestID)
		return validatedAsset{}, &AssetsResponse{StatusCode: http.StatusBadRequest, Body: []byte("No asset name provided")}
	}

	if req.RuntimeVersion == "" {
		log.Printf("[RequestID: %s] No runtime version provided", requestID)
		return validatedAsset{}, &AssetsResponse{StatusCode: http.StatusBadRequest, Body: []byte("No runtime version provided")}
	}

	if req.Update == nil {
		// The service resolves the update and 404s before reaching here, so
		// a nil Update means "no matching update". Guard it rather than
		// dereferencing into a SIGSEGV below.
		log.Printf("[RequestID: %s] No update found", requestID)
		return validatedAsset{}, &AssetsResponse{StatusCode: http.StatusNotFound, Body: []byte("No update found")}
	}

	metadata, err := update.GetMetadata(*req.Update)
	if errors.Is(err, update.ErrUpdateMetadataMissing) {
		// No metadata means no manifest ever advertised this asset: answer
		// like any unknown asset instead of surfacing a server error.
		log.Printf("[RequestID: %s] %v", requestID, err)
		return validatedAsset{}, &AssetsResponse{StatusCode: http.StatusNotFound, Body: []byte("Asset not found")}
	}
	if err != nil {
		log.Printf("[RequestID: %s] Error getting metadata: %v", requestID, err)
		return validatedAsset{}, &AssetsResponse{StatusCode: http.StatusInternalServerError, Body: []byte("Error getting metadata")}
	}

	platformMetadata, err := metadata.MetadataJSON.FileMetadata.GetPlatformMetadata(req.Platform)
	if err != nil || platformMetadata.Bundle == "" {
		log.Printf("[RequestID: %s] Error getting platform metadata: %v", requestID, err)
		return validatedAsset{}, &AssetsResponse{StatusCode: http.StatusBadRequest, Body: []byte("Platform not supported")}
	}
	isLaunchAsset := platformMetadata.Bundle == req.AssetName

	var assetMetadata types.Asset
	for _, asset := range platformMetadata.Assets {
		if asset.Path == req.AssetName {
			assetMetadata = asset
		}
	}

	if !isLaunchAsset && assetMetadata == (types.Asset{}) {
		log.Printf("[RequestID: %s] Asset not found in metadata: %s", requestID, req.AssetName)
		return validatedAsset{}, &AssetsResponse{StatusCode: http.StatusNotFound, Body: []byte("Asset not found")}
	}

	return validatedAsset{update: req.Update, isLaunchAsset: isLaunchAsset, assetMetadata: assetMetadata}, nil
}

func expoProtocolHeaders() map[string]string {
	return map[string]string{
		"expo-protocol-version": "1",
		"expo-sfv-version":      "0",
		"Cache-Control":         "public, max-age=31536000",
	}
}

func HandleAssetsWithFile(req AssetsRequest) (AssetsResponse, error) {
	validated, errResp := validateAssetRequest(req)
	if errResp != nil {
		return *errResp, nil
	}

	asset, err := bucket.GetBucket().GetFile(*validated.update, req.AssetName)
	if err != nil {
		log.Printf("[RequestID: %s] Error getting asset: %v", req.RequestID, err)
		return AssetsResponse{StatusCode: http.StatusInternalServerError, Body: []byte("Error getting asset")}, nil
	}
	if asset == nil {
		log.Printf("[RequestID: %s] Resolved file is nil", req.RequestID)
		return AssetsResponse{StatusCode: http.StatusInternalServerError, Body: []byte("Resolved file is nil")}, nil
	}

	buffer, err := bucket.ConvertReadCloserToBytes(asset.Reader)
	defer asset.Reader.Close()
	if err != nil {
		log.Printf("[RequestID: %s] Error converting asset to buffer: %v", req.RequestID, err)
		return AssetsResponse{StatusCode: http.StatusInternalServerError, Body: []byte("Error converting asset to buffer")}, err
	}

	contentType := update.AssetContentType(string(validated.assetMetadata.Ext), validated.isLaunchAsset)

	headers := expoProtocolHeaders()
	headers["Content-Type"] = contentType

	return AssetsResponse{
		StatusCode:  http.StatusOK,
		Headers:     headers,
		ContentType: contentType,
		Body:        buffer,
	}, nil
}

func HandleAssetsWithURL(req AssetsRequest, resolvedCDN cdn.CDN) (AssetsResponse, error) {
	validated, errResp := validateAssetRequest(req)
	if errResp != nil {
		return *errResp, nil
	}

	redirectURL, err := resolvedCDN.ComputeRedirectionURLForAsset(req.AppId, req.Branch, req.RuntimeVersion, validated.update.UpdateId, req.AssetName)
	if err != nil {
		log.Printf("[RequestID: %s] Error computing redirection URL: %v", req.RequestID, err)
		return AssetsResponse{StatusCode: http.StatusInternalServerError, Body: []byte("Error computing redirection URL")}, err
	}
	return AssetsResponse{
		StatusCode: http.StatusOK,
		Headers:    expoProtocolHeaders(),
		URL:        redirectURL,
	}, nil
}
