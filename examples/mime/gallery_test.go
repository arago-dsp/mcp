package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	galleryv1 "github.com/the-protobuf-project/mcp/examples/mime/gen/go/gallery/v1"
	"github.com/the-protobuf-project/runtime-go/agents/mcp"
)

// connect builds the gallery MCP server exactly as main does, serves it over a
// local HTTP transport, and returns a connected client session.
func connect(t *testing.T) (*mcp.ClientSession, context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	server := mcp.NewMCPServer(&mcp.MCPServerConfig{Name: "gallery-test", Version: "1.0.0"})
	galleryv1.RegisterGalleryServiceMCPHandler(server, &galleryServer{}, contentHandlers()...)

	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server }, nil,
	))
	t.Cleanup(ts.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

// Every declared media type must survive into the resource listing, since that
// is the only thing telling a client how to render each asset.
func TestListResourcesCarriesDeclaredMediaTypes(t *testing.T) {
	session, ctx := connect(t)

	res, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}

	got := make(map[string]*mcp.Resource, len(res.Resources))
	for _, r := range res.Resources {
		got[r.URI] = r
	}

	for _, want := range assets {
		r, ok := got[want.uri]
		if !ok {
			t.Errorf("resource %s missing from listing", want.uri)
			continue
		}
		if r.MIMEType != want.mimeType {
			t.Errorf("%s: MIMEType = %q, want %q", want.uri, r.MIMEType, want.mimeType)
		}
		if r.Title != want.title {
			t.Errorf("%s: Title = %q, want %q", want.uri, r.Title, want.title)
		}
	}
}

// Resource metadata declared in the proto — annotations and icons — has to
// reach the client, not just the media type.
func TestListResourcesCarriesAnnotationsAndIcons(t *testing.T) {
	session, ctx := connect(t)

	res, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}

	var overview *mcp.Resource
	for _, r := range res.Resources {
		if r.URI == "gallery://docs/overview.md" {
			overview = r
		}
	}
	if overview == nil {
		t.Fatal("overview resource missing")
	}
	if overview.Annotations == nil {
		t.Fatal("overview: Annotations = nil, want audience/priority from the proto")
	}
	if got, want := len(overview.Annotations.Audience), 2; got != want {
		t.Errorf("overview: %d audience entries, want %d", got, want)
	}
	if got, want := overview.Annotations.Priority, 1.0; got != want {
		t.Errorf("overview: Priority = %v, want %v", got, want)
	}
	if got, want := len(overview.Icons), 2; got != want {
		t.Errorf("overview: %d icons, want %d (a light and a dark variant)", got, want)
	}
}

// Textual media types must arrive as text and binary ones as a blob; a client
// picks how to present the bytes from the media type alone.
func TestReadResourceSplitsTextAndBinaryByMediaType(t *testing.T) {
	session, ctx := connect(t)

	for _, a := range assets {
		t.Run(a.id, func(t *testing.T) {
			res, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: a.uri})
			if err != nil {
				t.Fatalf("ReadResource(%s): %v", a.uri, err)
			}
			if len(res.Contents) != 1 {
				t.Fatalf("got %d contents, want 1", len(res.Contents))
			}
			c := res.Contents[0]
			if c.MIMEType != a.mimeType {
				t.Errorf("MIMEType = %q, want %q", c.MIMEType, a.mimeType)
			}
			if isTextual(a.mimeType) {
				if c.Text == "" {
					t.Error("textual media type returned no text")
				}
				if len(c.Blob) != 0 {
					t.Error("textual media type also returned a blob")
				}
				return
			}
			if len(c.Blob) == 0 {
				t.Error("binary media type returned no blob")
			}
			if c.Text != "" {
				t.Error("binary media type also returned text")
			}
		})
	}
}

// The PNG and PDF must survive the base64 round trip intact, or a client
// renders a broken image.
func TestBinaryAssetsRoundTrip(t *testing.T) {
	session, ctx := connect(t)

	for _, id := range []string{"logo", "spec"} {
		a, ok := assetByID(id)
		if !ok {
			t.Fatalf("asset %q missing", id)
		}
		res, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: a.uri})
		if err != nil {
			t.Fatalf("ReadResource(%s): %v", a.uri, err)
		}
		if got := res.Contents[0].Blob; string(got) != string(a.blob) {
			t.Errorf("%s: blob round trip changed the bytes (%d in, %d out)", id, len(a.blob), len(got))
		}
	}
}

// The UI resource the MCP App generates is HTML, which is what makes a client
// render it as an app surface rather than dumping markup.
func TestAppResourceIsHTML(t *testing.T) {
	session, ctx := connect(t)

	res, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	appURI := mcp.AppResourceURI("GalleryService")
	for _, r := range res.Resources {
		if r.URI == appURI {
			if r.MIMEType != "text/html" {
				t.Errorf("app resource MIMEType = %q, want text/html", r.MIMEType)
			}
			return
		}
	}
	t.Errorf("app resource %s missing from listing", appURI)
}

// The tool filter narrows on media type, so an unknown one must fail loudly
// rather than quietly returning nothing.
func TestListAssetsFiltersByMediaType(t *testing.T) {
	srv := &galleryServer{}

	got, err := srv.ListAssets(context.Background(), &galleryv1.ListAssetsRequest{Filter: `mime_type = "text/markdown"`})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if len(got.GetAssets()) != 1 || got.GetAssets()[0].GetName() != "assets/overview" {
		t.Errorf("filtering on text/markdown returned %v, want just assets/overview", got.GetAssets())
	}

	if _, err := srv.ListAssets(context.Background(), &galleryv1.ListAssetsRequest{Filter: `mime_type = "audio/flac"`}); err == nil {
		t.Error("filtering on an absent media type: got nil error, want one")
	}

	// A filter this gallery cannot interpret is refused rather than ignored,
	// so a caller never believes a narrowing was applied when it was not.
	if _, err := srv.ListAssets(context.Background(), &galleryv1.ListAssetsRequest{Filter: "size_bytes > 10"}); err == nil {
		t.Error("unsupported filter expression: got nil error, want one")
	}
}

// Paging walks the whole gallery: each page is capped at page_size and the
// final page stops handing out a token.
func TestListAssetsPaginates(t *testing.T) {
	srv := &galleryServer{}

	var seen []string
	token := ""
	for pages := 0; ; pages++ {
		if pages > len(assets) {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
		got, err := srv.ListAssets(context.Background(), &galleryv1.ListAssetsRequest{PageSize: 2, PageToken: token})
		if err != nil {
			t.Fatalf("ListAssets(page_token=%q): %v", token, err)
		}
		if n := len(got.GetAssets()); n > 2 {
			t.Fatalf("page held %d assets, want at most page_size=2", n)
		}
		for _, a := range got.GetAssets() {
			seen = append(seen, a.GetName())
		}
		if token = got.GetNextPageToken(); token == "" {
			break
		}
	}
	if len(seen) != len(assets) {
		t.Errorf("paging yielded %d assets (%v), want all %d", len(seen), seen, len(assets))
	}

	if _, err := srv.ListAssets(context.Background(), &galleryv1.ListAssetsRequest{PageToken: "not-a-number"}); err == nil {
		t.Error("invalid page_token: got nil error, want one")
	}
}

// GetAsset is addressed by resource name, and anything that is not a name in
// this collection is rejected before a lookup is attempted.
func TestGetAssetByResourceName(t *testing.T) {
	srv := &galleryServer{}

	got, err := srv.GetAsset(context.Background(), &galleryv1.GetAssetRequest{Name: "assets/overview"})
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if got.GetName() != "assets/overview" || got.GetText() == "" {
		t.Errorf("GetAsset returned name=%q text=%d bytes, want assets/overview with text", got.GetName(), len(got.GetText()))
	}

	for _, name := range []string{"overview", "assets/", "assets/nested/overview", "other/overview"} {
		if _, err := srv.GetAsset(context.Background(), &galleryv1.GetAssetRequest{Name: name}); err == nil {
			t.Errorf("GetAsset(%q): got nil error, want one", name)
		}
	}
}

// MCP 2026-07-28 tools carry an outputSchema describing what they return, and a
// result satisfies it through structuredContent. Declaring one without the other
// would advertise a contract the result does not meet, so both are checked here.
func TestToolsDeclareAnOutputSchema(t *testing.T) {
	session, ctx := connect(t)

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range res.Tools {
		if tool.OutputSchema == nil {
			t.Errorf("tool %q has no outputSchema", tool.Name)
		}
	}
}

func TestToolResultCarriesStructuredContent(t *testing.T) {
	session, ctx := connect(t)

	out, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_asset",
		Arguments: map[string]any{"name": "assets/overview"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if out.IsError {
		t.Fatalf("tool reported an error: %+v", out.Content)
	}
	if out.StructuredContent == nil {
		t.Fatal("result has no structuredContent, so its declared outputSchema is unsatisfied")
	}
	// The structured value must be the response message, not the text blob.
	obj, ok := out.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is %T, want an object", out.StructuredContent)
	}
	// GetAsset returns the Asset itself rather than a wrapper, so the resource's
	// own fields sit at the top level of the structured value.
	if obj["mime_type"] != "text/markdown" {
		t.Errorf("mime_type = %v, want text/markdown", obj["mime_type"])
	}
	if obj["name"] != "assets/overview" {
		t.Errorf("name = %v, want assets/overview", obj["name"])
	}
}

// Behavioural hints tell a client whether a call can be auto-approved. They are
// only useful if they survive to the wire, and the distinction between "stated
// false" and "not stated" has to survive with them.
func TestToolsCarryBehaviouralHints(t *testing.T) {
	session, ctx := connect(t)

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}

	listed, ok := byName["list_assets"]
	if !ok {
		t.Fatal("list_assets missing from tools/list")
	}
	if listed.Annotations == nil {
		t.Fatal("list_assets has no annotations, so a client cannot tell it is safe")
	}
	if !listed.Annotations.ReadOnlyHint {
		t.Error("readOnlyHint = false, want true for a listing tool")
	}
	if !listed.Annotations.IdempotentHint {
		t.Error("idempotentHint = false, want true")
	}
	// open_world: false is stated in the proto, so it must arrive as a non-nil
	// false rather than as an absent hint.
	if listed.Annotations.OpenWorldHint == nil {
		t.Error("openWorldHint is absent, but the proto states it explicitly")
	} else if *listed.Annotations.OpenWorldHint {
		t.Error("openWorldHint = true, want false")
	}
	// destructive is not stated, and must stay absent rather than default.
	if listed.Annotations.DestructiveHint != nil {
		t.Errorf("destructiveHint = %v, want absent — the proto does not state it",
			*listed.Annotations.DestructiveHint)
	}
}

// Display metadata — title and icons — is what a client shows a human. It has
// to reach every primitive that declares it, not just resources.
func TestDisplayMetadataReachesTheClient(t *testing.T) {
	session, ctx := connect(t)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var listed *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "list_assets" {
			listed = tool
		}
	}
	if listed == nil {
		t.Fatal("list_assets missing")
	}
	if listed.Title != "List assets" {
		t.Errorf("tool Title = %q, want %q", listed.Title, "List assets")
	}
	if len(listed.Icons) != 1 {
		t.Errorf("tool has %d icons, want 1", len(listed.Icons))
	} else if listed.Icons[0].Theme != "light" {
		t.Errorf("tool icon theme = %q, want light", listed.Icons[0].Theme)
	}

	prompts, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(prompts.Prompts) == 0 {
		t.Fatal("no prompts listed")
	}
	p := prompts.Prompts[0]
	if p.Title != "Summarise the gallery" {
		t.Errorf("prompt Title = %q", p.Title)
	}
	// A prompt argument's title comes from (mcp.v1.field).title on the schema
	// message, which is a different annotation reaching the same wire field.
	if len(p.Arguments) == 0 {
		t.Fatal("prompt has no arguments")
	}
	// Look the argument up by name: ListAssetsRequest leads with the pagination
	// fields, so the titled one is not first.
	var filterArg *mcp.PromptArgument
	for _, arg := range p.Arguments {
		if arg.Name == "filter" {
			filterArg = arg
			break
		}
	}
	if filterArg == nil {
		t.Fatalf("prompt has no `filter` argument; got %+v", p.Arguments)
	}
	if filterArg.Title != "Filter" {
		t.Errorf("prompt argument Title = %q, want %q", filterArg.Title, "Filter")
	}

	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	appURI := mcp.AppResourceURI("GalleryService")
	for _, r := range resources.Resources {
		if r.URI == appURI {
			if r.Title != "The Asset Gallery" {
				t.Errorf("app Title = %q", r.Title)
			}
			if len(r.Icons) != 1 {
				t.Errorf("app has %d icons, want 1", len(r.Icons))
			}
			return
		}
	}
	t.Errorf("app resource %s missing", appURI)
}

// Protocol revision 2026-07-28 made list and read results cacheable. The hints
// are only useful if they survive to the wire.
func TestCacheHintsReachTheClient(t *testing.T) {
	session, ctx := connect(t)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if tools.TTLMs != 300000 {
		t.Errorf("tools/list TTLMs = %d, want 300000", tools.TTLMs)
	}
	if tools.CacheScope != "public" {
		t.Errorf("tools/list CacheScope = %q, want public", tools.CacheScope)
	}

	// A resource that opts out must not inherit the service-wide TTL, or a
	// volatile response gets cached for five minutes.
	volatile, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gallery://data/downloads.csv"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if volatile.TTLMs != 0 {
		t.Errorf("opted-out resource TTLMs = %d, want 0", volatile.TTLMs)
	}
	if volatile.CacheScope != "private" {
		t.Errorf("opted-out resource CacheScope = %q, want private", volatile.CacheScope)
	}

	// One without its own hint still gets the service default.
	stable, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gallery://docs/overview.md"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if stable.TTLMs != 300000 {
		t.Errorf("default resource TTLMs = %d, want 300000", stable.TTLMs)
	}
}
