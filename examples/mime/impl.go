package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	galleryv1 "github.com/the-protobuf-project/mcp/examples/mime/gen/go/gallery/v1"
)

var _ galleryv1.GalleryServiceMCPServer = (*galleryServer)(nil)

type galleryServer struct{}

// assetNamePrefix is the collection segment of an asset's resource name, so a
// name reads "assets/overview".
const assetNamePrefix = "assets/"

// defaultPageSize is used when a caller asks for a page without saying how big.
const defaultPageSize = 10

// mimeTypeFilter matches the one filter expression this gallery understands,
// `mime_type = "text/markdown"`. AIP-160 describes a far richer language; a
// six-asset example only needs the single equality term, and anything else is
// rejected rather than silently ignored.
var mimeTypeFilter = regexp.MustCompile(`^\s*mime_type\s*=\s*"([^"]*)"\s*$`)

// parseFilter reads the media type out of a filter expression. An empty filter
// matches every asset.
func parseFilter(filter string) (string, error) {
	if strings.TrimSpace(filter) == "" {
		return "", nil
	}
	m := mimeTypeFilter.FindStringSubmatch(filter)
	if m == nil {
		return "", fmt.Errorf("unsupported filter %q; this gallery understands only `mime_type = \"<media type>\"`", filter)
	}
	return m[1], nil
}

// assetResourceName returns the relative resource name of an asset.
func assetResourceName(id string) string {
	return assetNamePrefix + id
}

// assetIDFromName reverses [assetResourceName], rejecting anything that is not
// a name in this collection.
func assetIDFromName(name string) (string, error) {
	id, ok := strings.CutPrefix(name, assetNamePrefix)
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", fmt.Errorf("name %q is not an asset resource name; expected %q", name, assetNamePrefix+"{asset}")
	}
	return id, nil
}

// metadataOf renders an asset without its content, which is what a listing
// carries; GetAsset is the call that returns the bytes.
func metadataOf(a asset) *galleryv1.Asset {
	return &galleryv1.Asset{
		Name:      assetResourceName(a.id),
		Title:     a.title,
		MimeType:  a.mimeType,
		Uri:       a.uri,
		SizeBytes: a.sizeBytes(),
	}
}

// ListAssets returns a page of the gallery, optionally narrowed by a filter on
// media type.
func (s *galleryServer) ListAssets(_ context.Context, req *galleryv1.ListAssetsRequest) (*galleryv1.ListAssetsResponse, error) {
	want, err := parseFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}

	matched := make([]asset, 0, len(assets))
	for _, a := range assets {
		if want != "" && a.mimeType != want {
			continue
		}
		matched = append(matched, a)
	}
	if len(matched) == 0 && want != "" {
		return nil, fmt.Errorf("no assets with media type %q; gallery holds %v", want, mimeTypes())
	}

	// The page token is the offset into the filtered listing. That is only
	// stable because this gallery is fixed at build time; a real service would
	// encode enough state to survive the collection changing between pages.
	start := 0
	if token := req.GetPageToken(); token != "" {
		start, err = strconv.Atoi(token)
		if err != nil || start < 0 || start > len(matched) {
			return nil, fmt.Errorf("invalid page_token %q", token)
		}
	}
	size := int(req.GetPageSize())
	if size <= 0 {
		size = defaultPageSize
	}
	end := min(start+size, len(matched))

	resp := &galleryv1.ListAssetsResponse{}
	for _, a := range matched[start:end] {
		resp.Assets = append(resp.Assets, metadataOf(a))
	}
	if end < len(matched) {
		resp.NextPageToken = strconv.Itoa(end)
	}
	return resp, nil
}

// GetAsset returns one asset, content included. Which field carries it — text
// or data — follows from the media type, and the caller is told the type either
// way.
func (s *galleryServer) GetAsset(_ context.Context, req *galleryv1.GetAssetRequest) (*galleryv1.Asset, error) {
	id, err := assetIDFromName(req.GetName())
	if err != nil {
		return nil, err
	}
	a, ok := assetByID(id)
	if !ok {
		return nil, fmt.Errorf("asset %q not found", req.GetName())
	}
	out := metadataOf(a)
	if isTextual(a.mimeType) {
		out.Text = a.text
	} else {
		out.Data = a.blob
	}
	return out, nil
}
