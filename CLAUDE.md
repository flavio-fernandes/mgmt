# mgmt - Real-time Configuration Management Engine and Language

## Notes
* Use "golang" instead of "go" when referring to the programming language in docs, code, or variable names.
* Always make git commits on a branch, never directly to git master.

## Project Overview
mgmt is a real-time, reactive configuration management engine written in golang.
Repository: https://github.com/purpleidea/mgmt/

Key subsystems:
- **Engine** - DAG-based resource execution (`engine/`)
- **Lang** - mcl compiler: lex/parse → AST → unification → reactive DAGE (`lang/`)
- **GAPI** - Frontend interface: lang, yaml, puppet, empty (`gapi/`)
- **pgraph** - Directed graph library (`pgraph/`)

## Build & Test

make build		# Build mgmt binary
make gofmt		# Format all of the golang code
./test/test-govet.sh	# Run all the vet tests
# List all the sub tests in the TestAstFunc2 function:
go test -count=1 github.com/purpleidea/mgmt/lang -run 'TestAstFunc2/' -short -v
# Run one particular test:
go test -count=1 github.com/purpleidea/mgmt/lang -run 'TestAstFunc2/some_test' -v

Regenerate lexer/parser: `cd lang && make build` (requires `nex`, `goyacc`, `ragel`)

## Code Style
- Always use tabs instead of spaces when indenting any programming language.
- **golang style**: tabs for indentation, `gofmt -s` + `goimports`
- **mcl style**: tabs for indentation
- **Receivers**: always named `obj`, almost always pointer receivers
- **Error wrapping**: `errwrap.Wrapf(err, "context message")`
- **Comments**: ~80 char line width; code lines can exceed when breaking hurts readability
- **Empty slices**: use `[]string{}` not `var x []string`
- **Commit format**: `topic: Capitalized message` (no trailing period)
  - Examples: `engine: Fix resource deadlock`, `lang: core: net: Add split host port function`

## Key File Paths

| Area | Path |
|------|------|
| Engine core | `engine/resources.go`, `engine/graph/engine.go`, `engine/graph/state.go` |
| Resources | `engine/resources/` (file.go, svc.go, pkg.go, etc.) |
| Traits | `engine/traits/` (base, edgeable, groupable, refreshable, etc.) |
| AutoEdge | `engine/graph/autoedge/autoedge.go` |
| AutoGroup | `engine/graph/autogroup/autogroup.go` |
| Lang pipeline | `lang/lang.go`, `lang/gapi/gapi.go` |
| Lexer/Parser | `lang/parser/lexer.nex`, `lang/parser/parser.y`, `lang/parser/lexparse.go` |
| AST | `lang/ast/structs.go` |
| Type system | `lang/types/type.go`, `lang/types/value.go` |
| Unification | `lang/unification/unification.go`, `lang/unification/fastsolver/` |
| Functions | `lang/funcs/funcs.go`, `lang/funcs/dage/dage.go` |
| Simple func scaffold | `lang/funcs/simple/simple.go` |
| Core builtins | `lang/core/` |
| FuncGen config | `lang/core/funcgen.yaml` |
| GAPI interface | `gapi/gapi.go` |
| Bootstrap | `main.go` → `cli/run.go` → `lib/main.go` |
| pgraph | `pgraph/pgraph.go` |
| Converger | `converger/converger.go` |

## Resource Registration Pattern

```go
func init() {
    engine.RegisterResource(Kind, func() engine.Res { return &MyRes{} })
}
```

Canonical example: `engine/resources/file.go`

## Function Registration Pattern

```go
simple.Register("concat", &simple.Scaffold{
    I: &simple.Info{Pure: true, Memo: true, Fast: true, Spec: true},
    T: types.NewType("func(a str, b str) str"),
    F: ConcatFunc,
})
```

## Architecture Flow

```
CLI → GAPI.Cli() → [mcl compiler if lang] → Deploy
  → lib.Main.Run() → GAPI.Next() streams graphs
  → engine.Load() → Validate() → Commit()
  → Resources: Watch() + CheckApply() loop
  → Converger detects stability
```
