# Notes: adding a `from` query expression to Go

Goal: support LINQ-style queries as an expression:

```go
var numQuery = from num in numbers where (num % 2) == 0 select num   // []int
```

Grammar:

```
FromExpr = "from" Name "in" Expr "where" Expr "select" Expr
           "from" X    "in" Y    "where" E    "select" A
```

The compiler handles source in stages, and each stage needs to know about the new
expression:

1. **scanner**: text -> tokens (no changes needed, see below)
2. **parser**: tokens -> syntax tree (AST)
3. **type checker (types2)**: checks the AST and works out types
4. **AST walker / noder**: converts the checked AST into the compiler's internal form (IR) ⏳ (current step)
5. backend: generates code

Current error:
```
my-tests/main.go:8:17: internal compiler error: unexpected expression: &{0x... 0x... ...} (*syntax.FromExpr)
```
from the `default` case in `noder/writer.go:1972`. The walker (`syntax/walk.go`) is fixed
(commit 3016768364). Now the noder needs to know what a query compiles to (see section 6).

---

## Files changed (vs. upstream commit 29cfee917f)

All paths are under `src/cmd/compile/internal/`.

| File | Change |
|---|---|
| `syntax/nodes.go` | new `FromExpr` node |
| `syntax/parser.go` | detect and parse queries (`operand`, `fromExpr`, `wantInQuery`) |
| `types2/expr.go` | `case *syntax.FromExpr` in `exprInternal` |
| `types2/from.go` | new file: `check.fromExpr` |
| `syntax/walk.go` | `case *FromExpr` so the tree walker visits its children |
| `syntax/tokens.go`, `token_string.go`, `scanner.go` | changed, then reverted, so no net change (see "Keywords") |

Also: `src/my-tests/main.go` (test program), `running-instructions.md` (how to build and run).

---

## 1. Keywords: tried, then reverted

**First attempt (commits d8e1b6db2c, 89c8063404):** added `_From`, `_In`, `_Where` as real
tokens in `tokens.go`, regenerated `token_string.go`, and grew `keywordMap` in `scanner.go`
from `1 << 6` to `1 << 7`.

**Why it broke:** anything that is a keyword can never be used as a variable name. The std
library uses `in`, `from` and `where` as names all the time (roughly 1900 / 1000 / 300 files),
so make.bash failed compiling the runtime:

```
runtime/metrics.go:39:15: syntax error: unexpected in, expected )
    compute func(in *statAggregate, out *metricValue)
```

**Fix (commit 985606096e): contextual keywords.** Take from, in and where back out of the
keyword list so they stay ordinary names. Then in the parser, treat a from identifier
specially only where it starts a query, and check for the names in / where with
`p.tok == _Name && p.lit == "in"` instead of `p.want(_In)`. select is already a keyword, so
it's fine.

The idea is that from, in and where stay ordinary names everywhere, and the parser only
treats them specially where a query could start. C# does exactly this for LINQ: from, where
and select are contextual keywords there, so old C# code with a variable named from still
compiles.

`tokens.go`, `token_string.go` and `scanner.go` are now identical to upstream.
`token_string.go` was restored with `git checkout 29cfee917f -- token_string.go`, because
stringer couldn't run while the toolchain was broken.

---

## 2. AST node: `syntax/nodes.go`

```go
FromExpr struct {
	X *Name // from X
	Y Expr  // in Y
	E Expr  // where E
	A Expr  // select A
	expr
}
```

- **Expression, not a statement** (commit c5506e5ee7), because a query produces a value that
  you assign (`var numQuery = from ...`).
- **`X` is a `*Name`, not an `Expr`.** X *declares* a new variable, like `i` in
  `for i := range s` or `x` in `func(x int)`. It's never evaluated. If it allowed Expr then
  these would also be valid:
  ```
  from a + b in numbers where ... select ...
  from f() in numbers where ... select ...
  from nums[0] in numbers where ... select ...
  ```
  With `p.name()` the parser rejects them right away, and the type checker always gets a
  plain identifier it can declare. (`RangeClause` uses `Lhs Expr` only because Go allows
  `for k, v = range m`, which assigns to existing things like `a[i]`. Queries have no
  assignment form.)
- **Position:** every node embeds `node{pos Pos}`. `s.pos` is used for error locations
  (`types2` reads it via `syntax.StartPos`) and later for line tables / stack traces.

---

## 3. Parser: `syntax/parser.go`

### Detecting a query: in `operand`, `case _Name:`

```go
case _Name:
	if p.lit == "from" {
		pos := p.pos()
		name := p.name()      // consume "from"
		if p.tok == _Name {   // from followed by a name is a query
			return p.fromExpr(pos)
		}
		return name           // otherwise it's just a normal variable
	}
	return p.name()
```

- **Why "from followed by a name" works:** in valid Go, an identifier inside an expression is
  never directly followed by another identifier. `from - 1`, `f(from)` and `from.x` are all
  still fine. Parameter lists like `func(in *T)` go through `paramDeclOrNil`, not `operand`,
  so they aren't affected.
- **Why consume `from` first:** the parser only sees one token at a time (`p.tok`), so it has
  to move past `from` to see what follows. That's why `fromExpr` takes `pos` instead of
  calling `p.want(_From)` itself.
- **Why pass `pos`:** by the time `fromExpr` runs, `p.pos()` points at `num`, not `from`.
  Using the saved `pos` makes errors point at the start of the query (`main.go:8:17`).
- This had to go into the *existing* `case _Name:`. A second `case _Name:` in the same
  switch is a "duplicate case" compile error.
- The old `case _From:` in `unaryExpr` was removed (no `_From` token anymore).

### Parsing the rest: `fromExpr`

```go
s.X = p.name()         // declared variable
p.wantInQuery("in")
s.Y = p.expr()
p.wantInQuery("where")
s.E = p.expr()
p.want(_Select)        // select is still a real keyword
s.A = p.expr()
```

`p.expr()` stops at a name it doesn't expect (`numbers where ...` -> parses `numbers`, stops
at `where`), which is what lets `wantInQuery` pick up the next word.

### Consuming tokens

Neither function consumes directly. `p.next()` is what moves the parser forward, and both call it.

- want(tok) checks that the current token is tok, moves past it and throws it away. It's for
  tokens whose only job is to be there, like ), { or _Select.
- name() checks that the current token is a _Name, saves its spelling in a *Name node, moves
  past it and returns the node. You need the node in the case where from turns out to be an
  ordinary variable.

### `wantInQuery(word)`: `want()` for contextual keywords

```go
if p.tok == _Name && p.lit == word {
	p.next()
	return
}
p.syntaxError("expected " + word)
p.advance()
```

- **`p.tok == _Name`:** all identifiers share the token type `_Name`. Only `p.lit` (the
  spelling) tells them apart. This check also guarantees `p.lit` is current: the scanner only
  sets `lit` for names, literals, semicolons and keywords (`scanner.go:39`), so on an
  operator like `+` it still holds the previous token's text.
- **`p.lit == word`:** without it, any name would be accepted (`from num banana numbers ...`).
- **`syntaxError("expected " + word)`:** reports the error. The `"expected "` prefix makes it
  print `syntax error: unexpected name inn, expected in`. Parsing continues afterwards (up to
  10 errors).
- **`advance()`:** with no arguments it skips exactly one token, so a typo like `inn` causes
  one error instead of a chain of follow-up errors.

### Limitation

Keep a query on one line. Go inserts a semicolon after a name at the end of a line, so a
newline before `where` ends the expression. (Real keywords would have the same problem.)

---

## 4. Type checking: `types2/expr.go` and `types2/from.go`

### `exprInternal` (`types2/expr.go`)

`exprInternal` is the type checker's main expression function: a big type switch on the
node type. Without a case, `FromExpr` hit `default:` and panicked with
`unknown expression type *syntax.FromExpr`. Added (same pattern as `CompositeLit`):

```go
case *syntax.FromExpr:
	check.fromExpr(x, e)
	if !x.isValid() {
		goto Error
	}
```

### `check.fromExpr` (`types2/from.go`), adapted from `rangeStmt` in `types2/range.go`

An **operand** is the type checker's record of a checked expression: the expression, its mode
(value, constant, type, invalid...) and its type. `x` is the operand for the whole query, so
setting `x.mode_` / `x.typ_` is how `fromExpr` "returns" its result.

Steps 1, 3 and 4 all have the same shape: **check an expression, then ask something about its
type.** Step 2 is different: it **creates** something.

1. **Check Y.** `check.expr` on `numbers` gives `[]int`. Is its underlying type a `*Slice` or
   `*Array`? If so, keep the element type (`elem = int`) for step 2. Otherwise, error
   `cannot query over ...`. This uses a 2-case switch instead of `rangeKeyVal`, which handles
   maps, strings, channels, ints and iterators too.
2. **Create X so E and A can find it.** When checking `num % 2`, `check.ident` looks `num`
   up with `lookupScope` (walks the current scope, then its parents). Without this step you
   get `undefined: num`.
   - `openScope(e, "from")`: start a new, empty dictionary for this query.
   - `defer closeScope()`: throw it away when `fromExpr` returns, so `num` doesn't leak
     outside the query (like a `for` loop variable).
   - `newVar(LocalVar, pos, pkg, "num", elem)`: make the variable, already knowing its type.
   - `declare(check.scope, e.X, obj, ...)`: put `num` -> that variable in the dictionary. It
     also calls `recordDef` (records that this identifier defines this object) and reports
     "redeclared" errors.
   - Y is checked *before* `openScope`, so `num` isn't visible inside Y (same as range).
3. **Check that E is a boolean.** Taken from the `if`/`for` conditions (`stmt.go:599`):
   `allBoolean(cond.typ())`, else `non-boolean condition in where clause`.
4. **Check A and make the whole query a slice of A's type.**
   `check.assignment(&a, nil, "select clause")` gives untyped constants their default type
   (`select 1` -> `int`; otherwise it would be a slice of "untyped int"). Then
   `x.mode_ = value`, `x.typ_ = NewSlice(a.typ())`.

```
fromExpr starts          scopes: [main's scope]
step 1: check numbers    finds "numbers" in main's scope -> []int, so elem = int
step 2: openScope        scopes: [main's scope] -> [from scope: empty]
        declare          scopes: [main's scope] -> [from scope: num=int]
step 3: check E          "num" found in from scope -> int, so num % 2 is fine
step 4: check A          "num" found again -> int
fromExpr returns         closeScope runs; scopes: [main's scope]
```

Notes:
- The `return` at the end of `fromExpr` is needed only because of the leftover
  `(isFuncInst bool)` result from the copied code. Removing that result removes the need.
- An unused X (`from num in xs where true select 0`) gives Go's normal
  "declared and not used" error. `check.usedVars[obj] = true` would silence it.

---

## 5. Tree walker: `syntax/walk.go` (commit 3016768364)

After type checking, the error was:
```
internal compiler error: panic: internal error: unknown node type *syntax.FromExpr
```
from the `default` case at `syntax/walk.go:312`. This meant **type checking passed**: the
walk is called at `noder/irgen.go:110`, right after `conf.Check(...)` (the type checker).

`walk.go` is the syntax tree's generic visitor (`syntax.Inspect` / `syntax.Walk`). For each
node type it knows which children to visit. Added, after `*ListExpr`, in source order:

```go
case *FromExpr:
	w.node(n.X)
	w.node(n.Y)
	w.node(n.E)
	w.node(n.A)
```

### Why `walk.go` and not `ir/node.go`: two separate trees

| | Syntax tree (`syntax`) | IR (`ir`) |
|---|---|---|
| Where | `syntax/nodes.go` | `ir/node.go` |
| Built by | the parser | the noder |
| Used by | `types2`, the noder | everything after: inlining, escape analysis, SSA, codegen |
| Shape | close to the source, one Go type per kind of syntax (`FromExpr`, `Operation`, ...) | one `Node` interface with an `Op` code (`OFOR`, `ORANGE`, `OAPPEND`, ...) |

```
source -> parser -> syntax tree -> types2 -> noder -> IR -> ... -> machine code
                    (FromExpr)                       (ir.Node)
```

`FromExpr` only exists in the syntax tree. The panic was in the syntax tree's walker, before
anything reached IR, so the fix belongs in `syntax/walk.go`.

**Naming trap:** `cmd/compile/internal/walk` is a *different* thing: a late compiler pass
over IR. `syntax/walk.go` is just a tree visitor for the syntax tree.

---

## 6. Noder: what a query compiles to ⏳ (in progress)

### Where the current error comes from

`noder/writer.go:1972`, the `default` case of `writer.expr`'s type switch:

```go
switch expr := expr.(type) {
default:
	w.p.unexpected("expression", expr)   // <- current error
case *syntax.CompositeLit:
	w.Code(exprCompLit)
	w.compLit(expr)
...
```

Same pattern as the last errors: a switch over node types with no case for ours. The
`&{0x4641...}` is just the `FromExpr` struct's pointer fields printed.

### How the noder works

```
syntax tree + types ──writer.go──> compact binary encoding ──reader.go──> IR
```

- **`writer.go`** goes through the checked syntax tree and writes each expression as a code
  from `codes.go` (`exprCompLit`, `exprFuncLit`, `exprBinaryOp`, ...) followed by its parts.
- **`reader.go`** reads the codes back and builds IR nodes. Its matching switch is at
  `reader.go:2208` (`panic("unhandled expression")` for unknown codes).
- The same encoding is used to save packages for importing and inlining, which is why there's
  a writer and a reader even inside one compile.
- The reader must read things back in **exactly the order** the writer wrote them.

### Plan: three changes

1. **`noder/codes.go`:** add `exprFrom` at the **end** of the `codeExpr` list (after
   `exprRuntimeBuiltin`), so existing codes keep their numbers.
2. **`noder/writer.go`:** `case *syntax.FromExpr:` writes `exprFrom`, then the parts:
   position, result type, `Y`, the variable `X`, `E`, `A`.
3. **`noder/reader.go`:** `case exprFrom:` reads the parts back and builds the meaning out of
   IR nodes that already exist.

### The meaning (built in the reader)

```go
// from num in numbers where (num % 2) == 0 select num
var tmp []int                    // typecheck.TempAt(...)          typecheck/dcl.go:31
for _, num := range numbers {    // ir.NewRangeStmt(...)           ir/stmt.go:407
	if (num % 2) == 0 {          // ir.NewIfStmt(...)              ir/stmt.go:272
		tmp = append(tmp, num)   // ir.NewAssignStmt(tmp, append)  ir/stmt.go:97
	}
}
// value of the expression: tmp
```

- **`ir.InitExpr(init, expr)`** (`ir/node.go:522`) attaches statements to an expression. It's
  how an expression can contain a loop: the loop goes in `init`, and `tmp` is the value.
- Because the result uses only ordinary loops, `if`s and `append`s, nothing after the noder
  needs to know queries exist, so **no new `Op` in `ir/node.go`**.
- The trickiest part is `X`: it must be declared as a local in the writer and created in the
  reader, like range loop variables. Model: `writer.forStmt` (`writer.go:1599`) and
  `reader.forStmt` (`reader.go:1926`), which use `openScope`, `assignList`, `initDefn`.
- Start with `codes.go` + the writer case, since the reader has to match the writer's order.

### Alternative (not chosen): rewrite the syntax tree

Range-over-func loops are rewritten *in the syntax tree* after type checking by
`rangefunc.Rewrite` (`noder/irgen.go:214`). We could rewrite `FromExpr` into ordinary syntax
there instead. The catch is that every new syntax node needs its type info recorded by hand,
because `types2` has already run. The writer/reader approach is the usual way expressions get
added and keeps everything in the noder.

---

## Next steps / TODO

- [x] **`syntax/walk.go`:** add a case so the tree walker can visit `FromExpr`'s children
      (commit 3016768364).
- [ ] **noder:** `codes.go` (`exprFrom`), `writer.go` case, `reader.go` case that builds the
      range / if / append loop (section 6).
- [ ] `syntax/printer.go`: add a case so the AST can be printed (debug output).
- [ ] `syntax/positions.go` `EndPos`: add `case *FromExpr: m = n.A`, so the scope's recorded
      end is correct. Not required, since lookup doesn't use it.
- [ ] Cleanup in `from.go`: drop `(isFuncInst bool)` and fix the doc comment ("range
      statement" -> "query expression", and use `//` on the blank lines).
