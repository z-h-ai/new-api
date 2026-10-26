package billingexpr

import (
	"fmt"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"math"
)

type costInterval struct{ low, high float64 }

// ReservationUpperBound bounds the existing expression over text token limits.
// It never prices actual usage. Both conditional branches are covered, including
// peak periods and context tiers. Unknown arithmetic is refused, not estimated
// as zero. Cache categories are independently bounded, conservatively retaining
// overlap rather than assuming an upstream cache split before execution.
func ReservationUpperBound(expression string, input, output int) (float64, error) {
	if input < 0 || output < 0 || len(expression) > 32768 {
		return 0, fmt.Errorf("invalid reservation bounds")
	}
	if _, err := CompileFromCache(expression); err != nil {
		return 0, err
	}
	version, body := ParseExprVersion(expression)
	if version != 1 {
		return 0, fmt.Errorf("unsupported billing version")
	}
	tree, err := parser.Parse(body)
	if err != nil {
		return 0, err
	}
	interval, err := boundCost(tree.Node, float64(input), float64(output), 0)
	if err != nil {
		return 0, err
	}
	if interval.low < 0 || math.IsNaN(interval.high) || math.IsInf(interval.high, 0) {
		return 0, fmt.Errorf("unbounded or negative reservation")
	}
	return interval.high, nil
}

func boundCost(node ast.Node, input, output float64, depth int) (costInterval, error) {
	if depth > 128 {
		return costInterval{}, fmt.Errorf("reservation expression too deep")
	}
	switch n := node.(type) {
	case *ast.IntegerNode:
		return costInterval{float64(n.Value), float64(n.Value)}, nil
	case *ast.FloatNode:
		return costInterval{n.Value, n.Value}, nil
	case *ast.IdentifierNode:
		switch n.Value {
		case "p", "len", "cr", "cc", "cc1h", "img", "img_cr", "ai":
			return costInterval{0, input}, nil
		case "c", "img_o", "ao":
			return costInterval{0, output}, nil
		}
	case *ast.ConditionalNode:
		// The compiled condition selects one branch; taking their union is safe
		// without evaluating request probes or choosing today's cheap tariff.
		// Conditions contain probes (param/header/hour) and comparisons, which
		// are booleans, never cost terms, so they are not traversed.
		a, err := boundCost(n.Exp1, input, output, depth+1)
		if err != nil {
			return a, err
		}
		b, err := boundCost(n.Exp2, input, output, depth+1)
		if err != nil {
			return b, err
		}
		return costInterval{math.Min(a.low, b.low), math.Max(a.high, b.high)}, nil
	case *ast.UnaryNode:
		a, err := boundCost(n.Node, input, output, depth+1)
		if err != nil {
			return a, err
		}
		if n.Operator == "+" {
			return a, nil
		}
		if n.Operator == "-" {
			return costInterval{-a.high, -a.low}, nil
		}
	case *ast.BinaryNode:
		a, err := boundCost(n.Left, input, output, depth+1)
		if err != nil {
			return a, err
		}
		b, err := boundCost(n.Right, input, output, depth+1)
		if err != nil {
			return b, err
		}
		var v costInterval
		switch n.Operator {
		case "+":
			v = costInterval{a.low + b.low, a.high + b.high}
		case "-":
			v = costInterval{a.low - b.high, a.high - b.low}
		case "*", "/":
			if n.Operator == "/" {
				if b.low <= 0 && b.high >= 0 {
					return v, fmt.Errorf("reservation division crosses zero")
				}
				b = costInterval{1 / b.high, 1 / b.low}
			}
			values := []float64{a.low * b.low, a.low * b.high, a.high * b.low, a.high * b.high}
			v = costInterval{values[0], values[0]}
			for _, x := range values[1:] {
				v.low = math.Min(v.low, x)
				v.high = math.Max(v.high, x)
			}
		default:
			return v, fmt.Errorf("unsupported reservation operator %s", n.Operator)
		}
		// Outward rounding keeps the quote conservative under float arithmetic.
		if v.low != 0 {
			v.low = math.Nextafter(v.low, math.Inf(-1))
		}
		if v.high != 0 {
			v.high = math.Nextafter(v.high, math.Inf(1))
		}
		return v, nil
	case *ast.CallNode:
		name, ok := n.Callee.(*ast.IdentifierNode)
		if !ok {
			break
		}
		if name.Value == "tier" && len(n.Arguments) == 2 {
			return boundCost(n.Arguments[1], input, output, depth+1)
		}
		if name.Value == "fixed" && len(n.Arguments) == 1 {
			a, err := boundCost(n.Arguments[0], input, output, depth+1)
			return costInterval{a.low * 1e6, a.high * 1e6}, err
		}
		if (name.Value == "min" || name.Value == "max") && len(n.Arguments) == 2 {
			a, err := boundCost(n.Arguments[0], input, output, depth+1)
			if err != nil {
				return a, err
			}
			b, err := boundCost(n.Arguments[1], input, output, depth+1)
			if err != nil {
				return b, err
			}
			if name.Value == "min" {
				return costInterval{math.Min(a.low, b.low), math.Min(a.high, b.high)}, nil
			}
			return costInterval{math.Max(a.low, b.low), math.Max(a.high, b.high)}, nil
		}
	case *ast.BuiltinNode:
		// expr represents min/max as builtins on some engine versions.
		return boundCost(&ast.CallNode{Callee: &ast.IdentifierNode{Value: n.Name}, Arguments: n.Arguments}, input, output, depth+1)
	}
	return costInterval{}, fmt.Errorf("unsupported reservation expression node %T", node)
}
