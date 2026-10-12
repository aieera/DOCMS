package archtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestPhaseC5_TypeAwareGuardCatchesFixture proves the C5 guard resolves
// receivers by TYPE: every NATS publish in testdata/c5fixture is reported
// whatever the receiver is called (conn, s.bus.conn, an embedded *nats.Conn,
// a method value), and a non-NATS Publish method is not.
func TestPhaseC5_TypeAwareGuardCatchesFixture(t *testing.T) {
	var got []string
	for _, s := range natsPublishSites(t, ".", "./testdata/c5fixture") {
		got = append(got, fmt.Sprintf("%s:%d %s", filepath.Base(s.file), s.line, s.method))
	}
	want := []string{
		"fixture.go:30 (*nats.Conn).PublishMsg",
		"fixture.go:31 (*nats.Conn).Publish",
		"fixture.go:32 (nats.JetStream).PublishAsync",
		"fixture.go:33 (jetstream.Publisher).Publish",
		"fixture.go:34 (*nats.Conn).Publish",
		"fixture.go:35 (*nats.Conn).Publish",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fixture sites:\n got  %q\n want %q", got, want)
	}
}

// ---- type-aware NATS publish finder (C5) ----------------------------

// natsPackages are the packages whose Publish* methods are a direct NATS
// publish: *nats.Conn, nats.JetStreamContext and jetstream.JetStream (and
// anything that promotes or embeds them) all resolve to a method declared
// in one of these.
var natsPackages = map[string]bool{
	"github.com/nats-io/nats.go":           true,
	"github.com/nats-io/nats.go/jetstream": true,
}

// publishSite is one selector that resolves to a NATS Publish* method.
type publishSite struct {
	file   string // absolute path
	line   int
	method string // e.g. "(*nats.Conn).PublishMsg"
}

// goListPkg is the subset of `go list -json` the guard needs.
type goListPkg struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	Export     string
	ImportMap  map[string]string
	DepOnly    bool
	Error      *struct{ Err string }
}

// natsPublishSites type-checks the packages matched by patterns (resolved
// from dir; dependencies come from compiler export data produced by
// `go list -export -deps`, so only the matched packages are checked from
// source) and returns every selector — call, method value or method
// expression — whose method is a Publish* declared in a NATS package. The
// receiver's NAME is irrelevant: s.bus.Publish, conn.PublishMsg and a
// promoted e.Publish are all found when the receiver's type is NATS, and a
// Redis client's Publish is not.
func natsPublishSites(t *testing.T, dir string, patterns ...string) []publishSite {
	t.Helper()
	if len(patterns) == 0 {
		return nil
	}
	pkgs := goListExport(t, dir, patterns...)

	exports := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	fset := token.NewFileSet()
	gc := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		f, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(f)
	})

	var sites []publishSite
	for _, p := range pkgs {
		if p.DepOnly {
			continue
		}
		if p.Error != nil {
			// A pattern dir whose every file is behind a build tag
			// (e.g. integration-only) has nothing to check.
			if strings.Contains(p.Error.Err, "build constraints exclude all Go files") {
				continue
			}
			t.Fatalf("go list %s: %s", p.ImportPath, p.Error.Err)
		}
		if len(p.CgoFiles) > 0 {
			t.Fatalf("%s uses cgo; the C5 guard cannot type-check it from source — extend the guard", p.ImportPath)
		}
		var files []*ast.File
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			files = append(files, f)
		}
		info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
		conf := types.Config{Importer: importMapper{gc, p.ImportMap}}
		if _, err := conf.Check(p.ImportPath, fset, files, info); err != nil {
			t.Fatalf("type-check %s: %v", p.ImportPath, err)
		}
		for sel, s := range info.Selections {
			if s.Kind() != types.MethodVal && s.Kind() != types.MethodExpr {
				continue
			}
			fn := s.Obj()
			if fn.Pkg() == nil || !natsPackages[fn.Pkg().Path()] || !strings.HasPrefix(fn.Name(), "Publish") {
				continue
			}
			pos := fset.Position(sel.Sel.Pos())
			sites = append(sites, publishSite{file: pos.Filename, line: pos.Line, method: methodName(fn)})
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].file != sites[j].file {
			return sites[i].file < sites[j].file
		}
		return sites[i].line < sites[j].line
	})
	return sites
}

// methodName renders fn as "(<receiver>).<name>" with package-name
// qualifiers, e.g. "(*nats.Conn).Publish".
func methodName(fn types.Object) string {
	recv := "?"
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv = types.TypeString(sig.Recv().Type(), func(p *types.Package) string { return p.Name() })
	}
	return "(" + recv + ")." + fn.Name()
}

// importMapper applies a package's go-list ImportMap (vendoring / test
// variants) before delegating to the export-data importer.
type importMapper struct {
	types.Importer
	m map[string]string
}

func (i importMapper) Import(path string) (*types.Package, error) {
	if mapped, ok := i.m[path]; ok {
		path = mapped
	}
	return i.Importer.Import(path)
}

// goListExport runs `go list -e -export -deps -json` in dir.
func goListExport(t *testing.T, dir string, patterns ...string) []goListPkg {
	t.Helper()
	args := append([]string{"list", "-e", "-export", "-deps",
		"-json=ImportPath,Dir,GoFiles,CgoFiles,Export,ImportMap,DepOnly,Error"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -export: %v\n%s", err, stderr.String())
	}
	var pkgs []goListPkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p goListPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}
