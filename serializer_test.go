package openapigo

import (
	"net/url"
	"testing"
)

// --- Path style tests ---

type labelPathReq struct {
	Color string `path:"color,label"`
}

type labelPathArrayReq struct {
	Colors []string `path:"colors,label"`
}

type labelPathArrayExplodeReq struct {
	Colors []string `path:"colors,label,explode"`
}

type matrixPathReq struct {
	Color string `path:"color,matrix"`
}

type matrixPathArrayReq struct {
	Colors []string `path:"colors,matrix"`
}

type matrixPathArrayExplodeReq struct {
	Colors []string `path:"colors,matrix,explode"`
}

func TestBuildPath_LabelScalar(t *testing.T) {
	got := buildPath("/items/{color}", labelPathReq{Color: "blue"})
	want := "/items/.blue"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildPath_LabelArray(t *testing.T) {
	got := buildPath("/items/{colors}", labelPathArrayReq{Colors: []string{"blue", "black", "brown"}})
	want := "/items/.blue,black,brown"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildPath_LabelArrayExplode(t *testing.T) {
	got := buildPath("/items/{colors}", labelPathArrayExplodeReq{Colors: []string{"blue", "black", "brown"}})
	want := "/items/.blue.black.brown"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildPath_MatrixScalar(t *testing.T) {
	got := buildPath("/items/{color}", matrixPathReq{Color: "blue"})
	want := "/items/;color=blue"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildPath_MatrixArray(t *testing.T) {
	got := buildPath("/items/{colors}", matrixPathArrayReq{Colors: []string{"blue", "black", "brown"}})
	want := "/items/;colors=blue,black,brown"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildPath_MatrixArrayExplode(t *testing.T) {
	got := buildPath("/items/{colors}", matrixPathArrayExplodeReq{Colors: []string{"blue", "black", "brown"}})
	want := "/items/;colors=blue;colors=black;colors=brown"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Query style tests ---

type spaceDelimitedReq struct {
	Colors []string `query:"colors,spaceDelimited,noexplode"`
}

type pipeDelimitedReq struct {
	Colors []string `query:"colors,pipeDelimited,noexplode"`
}

type deepObjectReq struct {
	Filter map[string]string `query:"filter,deepObject"`
}

type formExplodeMapReq struct {
	Color map[string]string `query:"color"`
}

type formNoExplodeMapReq struct {
	Color map[string]string `query:"color,noexplode"`
}

func TestBuildQuery_SpaceDelimited(t *testing.T) {
	u, _ := url.Parse("http://example.com/items")
	buildQuery(u, spaceDelimitedReq{Colors: []string{"blue", "black"}})
	// url.Values.Encode() uses + for spaces.
	got := u.Query().Get("colors")
	want := "blue black"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildQuery_PipeDelimited(t *testing.T) {
	u, _ := url.Parse("http://example.com/items")
	buildQuery(u, pipeDelimitedReq{Colors: []string{"blue", "black"}})
	got := u.RawQuery
	// url.Values.Encode will encode the pipe character
	decoded, _ := url.QueryUnescape(got)
	want := "colors=blue|black"
	if decoded != want {
		t.Errorf("decoded %q, want %q (raw: %q)", decoded, want, got)
	}
}

func TestBuildQuery_DeepObject(t *testing.T) {
	u, _ := url.Parse("http://example.com/items")
	buildQuery(u, deepObjectReq{Filter: map[string]string{"type": "sale", "color": "red"}})
	q := u.Query()
	if got := q.Get("filter[color]"); got != "red" {
		t.Errorf("filter[color] = %q, want %q", got, "red")
	}
	if got := q.Get("filter[type]"); got != "sale" {
		t.Errorf("filter[type] = %q, want %q", got, "sale")
	}
}

func TestBuildQuery_FormExplodeMap(t *testing.T) {
	u, _ := url.Parse("http://example.com/items")
	buildQuery(u, formExplodeMapReq{Color: map[string]string{"R": "100", "G": "200"}})
	q := u.Query()
	if got := q.Get("R"); got != "100" {
		t.Errorf("R = %q, want %q", got, "100")
	}
	if got := q.Get("G"); got != "200" {
		t.Errorf("G = %q, want %q", got, "200")
	}
}

func TestBuildQuery_FormNoExplodeMap(t *testing.T) {
	u, _ := url.Parse("http://example.com/items")
	buildQuery(u, formNoExplodeMapReq{Color: map[string]string{"R": "100", "G": "200"}})
	got := u.Query().Get("color")
	// sorted: G,200,R,100
	want := "G,200,R,100"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Style tag parsing tests ---

type styleTagReq struct {
	A string `path:"a"`
	B string `path:"b,label"`
	C string `path:"c,matrix"`
	D string `query:"d"`
	E string `query:"e,deepObject"`
}

func TestParseStructMeta_StyleTags(t *testing.T) {
	meta := parseStructMeta(typeOf[styleTagReq]())
	tests := []struct {
		name  string
		style string
	}{
		{"a", "simple"},
		{"b", "label"},
		{"c", "matrix"},
		{"d", "form"},
		{"e", "deepObject"},
	}
	for _, tt := range tests {
		for _, fm := range meta.fields {
			if fm.name == tt.name {
				if fm.style != tt.style {
					t.Errorf("field %q: style = %q, want %q", tt.name, fm.style, tt.style)
				}
				break
			}
		}
	}
}
