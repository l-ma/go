// This file implements typechecking of from expressions

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

// fromExpr type-checks a range statement of form

// 	from X in Y where E select A

// and sets x to a value of type []T, where T is the type of A.
// adapted from src/cmd/compile/internal/types2/range.go
func (check *Checker) fromExpr(x *operand, e *syntax.FromExpr) (isFuncInst bool) {
	// check the data structure Y first (what it is and what type its elements are)
	var y operand
	check.expr(nil, &y, e.Y) // check type of data structure
	if !y.isValid() {
		x.invalidate()
		return
	}
	var elem Type
	switch t := y.typ().Underlying().(type) {
	case *Slice:
		elem = t.elem
	case *Array:
		elem = t.elem
	default:
		check.errorf(&y, InvalidRangeExpr, "cannot query over %s", &y)
		x.invalidate()
		return
	}

	// create X so that E and A can find it
	check.openScope(e, "from")                                     // start a new, empty dictionary for this query
	defer check.closeScope()                                       // throw it away when fromExpr returns
	obj := newVar(LocalVar, e.X.Pos(), check.pkg, e.X.Value, elem) // make a variable (in our first example: name "num", type int)
	check.declare(check.scope, e.X, obj, syntax.StartPos(e.E))     // put "num" -> that variable in the dictionary

	// check that E is a boolean
	// taken from if and for statements (stmt.go:599)
	var cond operand
	check.expr(nil, &cond, e.E)
	if cond.isValid() && !allBoolean(cond.typ()) {
		check.error(e.E, InvalidCond, "non-boolean condition in where clause")
	}

	// check A and make the whole query a slice of A's type
	// (in our first example: A is "num", an int, so the query is []int)
	var a operand
	check.expr(nil, &a, e.A)
	check.assignment(&a, nil, "select clause")
	if !a.isValid() {
		x.invalidate()
		return
	}
	x.mode_ = value
	x.typ_ = NewSlice(a.typ())
	return
}

// fromExpr starts          scopes: [main's scope]
// step 1: check numbers    finds "numbers" in main's scope -> []int, so elem = int
// step 2: openScope        scopes: [main's scope] -> [from scope: empty]
//         declare          scopes: [main's scope] -> [from scope: num=int]
// step 3: check E          "num" found in from scope -> int, so num % 2 is fine
// step 4: check A          "num" found again -> int
// fromExpr returns         closeScope runs; scopes: [main's scope]
