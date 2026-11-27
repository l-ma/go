# Running instructions

## 1. Set up PATH (every new terminal)

```
export PATH="$HOME/Desktop/projects/go/bin:$HOME/go/bin:$PATH"
```

- `~/Desktop/projects/go/bin` -> this repo's `go` (1.28-devel). Without it, `go` is
  Homebrew's 1.27.3, which fails inside `src/` with
  `go: download go1.28.0 for darwin/arm64: toolchain not available`.
- `~/go/bin` -> `stringer` (needed to regenerate `token_string.go`).

Check with `which go` (should be `/Users/laramarinov/Desktop/projects/go/bin/go`)
and `which stringer` (should be `/Users/laramarinov/go/bin/stringer`).

## 2. Normal loop: rebuild compiler and run tiny program on it

```
cd src
go install cmd/compile
go run my-tests/main.go
```

`go install cmd/compile` builds the new compiler using the one already in
`pkg/tool/darwin_arm64/compile`, so that one has to be working (see step 3).

## 3. Full rebuild with make.bash

Use this when:
- `go install cmd/compile` fails with
  `compile: version "go1.27.3-..." does not match go tool version "go1.28-devel..."`.
  That means an earlier make.bash died partway and left the temporary bootstrap
  compiler in `pkg/tool`.
- after changing tokens / the scanner, to rebuild the whole toolchain and std library.

```
cd src
GOROOT_BOOTSTRAP=/opt/homebrew/opt/go/libexec ./make.bash
```

`GOROOT_BOOTSTRAP` points at Homebrew's Go, which is used to build the first
stage. make.bash compiles the whole standard library with the modified compiler,
so syntax changes that break existing Go code show up here.

## 4. Regenerate token_string.go (after editing tokens.go)

Must be run from the `syntax` directory. From anywhere else you get
`cannot find module providing package tokens.go: import lookup disabled by -mod=vendor`.

```
cd src/cmd/compile/internal/syntax
go generate tokens.go
```

- Needs `stringer` on PATH (step 1).
- Remove all uses of deleted tokens first (parser, scanner), since stringer
  loads the whole package.
- Check the result with `git diff token_string.go`.

## Notes on the `from` expression

- `from`, `in`, `where` are contextual keywords, not tokens. The scanner is
  unchanged from stock Go and returns them as `_Name` with `p.lit` set. Making them
  real keywords broke the std library, e.g.
  `runtime/metrics.go:39:15: syntax error: unexpected in, expected )`,
  because `in` / `from` / `where` are used as variable names everywhere.
- The parser detects a query in `operand`: a `_Name` with `p.lit == "from"` followed
  by another `_Name`. `in` / `where` are matched with
  `p.tok == _Name && p.lit == word`. `select` is still the real `_Select` keyword.
- Keep a query on one line: Go inserts a semicolon after a name at the end of a line.
- New node kinds may also need cases in `syntax/walk.go` and `syntax/printer.go`
  (otherwise "unknown node" panics when the tree is walked or printed).
- Next step: type checking (`case *syntax.FromExpr` in `types2/expr.go`, `types2/from.go`).
