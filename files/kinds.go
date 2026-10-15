package main

import "strings"

func set(s string) map[string]bool {
	m := map[string]bool{}
	for _, e := range strings.Fields(s) {
		m[e] = true
	}
	return m
}

var (
	codeExts = set(`go mod sum js mjs cjs jsx ts tsx mts cts vue svelte astro py pyw rb php java kt kts scala groovy gradle c h cc cpp cxx hpp hh hxx
		cs fs fsx vb rs zig nim d dart swift m mm lua pl pm r jl hs elm ex exs erl clj cljs lisp el scm ml mli
		sh bash zsh fish ps1 psm1 psd1 bat cmd nu awk sed sql graphql gql proto thrift
		html htm xhtml css scss sass less styl xml xsl xsd svg json jsonc json5 yaml yml toml ini cfg conf env properties
		dockerfile makefile mk cmake nix tf hcl lock vim glsl hlsl wgsl shader asm s v sv vhd vhdl tex bib
		csproj vcxproj sln props targets resx xaml plist reg editorconfig gitignore gitattributes diff patch`)
	textExts    = set(`txt text md markdown mdx rst adoc org log csv tsv nfo me readme license srt vtt ass sub ` + keys(codeExts))
	imageExts   = set(`jpg jpeg jfif png gif webp bmp ico svg avif apng tif tiff heic heif`)
	videoExts   = set(`mp4 m4v webm mkv mov avi wmv flv mpg mpeg ts 3gp ogv`)
	audioExts   = set(`mp3 wav flac ogg oga opus m4a aac wma aiff aif mid midi`)
	archiveExts = set(`zip 7z rar tar gz tgz bz2 tbz xz txz zst cab iso jar apk nupkg vsix whl msix appx`)
	docExts     = set(`pdf doc docx odt rtf xls xlsx ods ppt pptx odp epub pages numbers key`)
	fontExts    = set(`ttf otf woff woff2`)
	exeExts     = set(`exe msi com scr appimage lnk url`)
)

func keys(m map[string]bool) string {
	var b strings.Builder
	for k := range m {
		b.WriteString(k)
		b.WriteByte(' ')
	}
	return b.String()
}

// kindOf: the UI's icon/preview class of a file.
func kindOf(name string, dir bool) string {
	if dir {
		return "dir"
	}
	e := extOf(name, false)
	switch {
	case e == "pdf":
		return "pdf"
	case imageExts[e]:
		return "image"
	case videoExts[e]:
		return "video"
	case audioExts[e]:
		return "audio"
	case archiveExts[e]:
		return "archive"
	case docExts[e]:
		return "doc"
	case fontExts[e]:
		return "font"
	case exeExts[e]:
		return "exe"
	case e == "md" || e == "markdown" || e == "mdx":
		return "markdown"
	case codeExts[e]:
		return "code"
	case textExts[e]:
		return "text"
	}
	switch strings.ToLower(name) {
	case "makefile", "dockerfile", "license", "readme", "go.mod", ".gitignore", ".env", "cmakelists.txt", "pkgbuild":
		return "code"
	}
	return "file"
}

func kindExts(kind string) []string {
	var m map[string]bool
	switch kind {
	case "image", "img", "picture":
		m = imageExts
	case "video", "movie":
		m = videoExts
	case "audio", "music":
		m = audioExts
	case "archive", "zip":
		m = archiveExts
	case "doc", "document":
		m = docExts
	case "code":
		m = codeExts
	case "text":
		m = textExts
	case "font":
		m = fontExts
	case "exe", "app":
		m = exeExts
	case "pdf":
		return []string{"pdf"}
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
