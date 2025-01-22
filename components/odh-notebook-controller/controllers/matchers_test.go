package controllers

import (
	"fmt"
	"github.com/google/go-cmp/cmp"
	"github.com/onsi/gomega/types"
)

// See https://onsi.github.io/gomega/#adding-your-own-matchers

// BeMatchingK8sResource is a custom Gomega matcher that compares using `comparator` function and reports differences using
// [cmp.Diff]. It attempts to minimize the diff (TODO(jdanek): not yet implemented) to only include those entries that cause `comparator` to fail.
//
// Use this to replace assertions such as
// > Expect(CompareNotebookRoutes(*route, expectedRoute)).Should(BeTrueBecause(cmp.Diff(*route, expectedRoute)))
// with
// > Expect(*route).To(BeMatchingK8sResource(expectedRoute, CompareNotebookRoutes))
//
// NOTE: The diff minimization functionality (TODO(jdanek): not yet implemented) is best-effort. It is designed to never under-approximate, but over-approximation is possible.
// NOTE2: Using [gcustom.MakeMatcher] is not possible because it does not conveniently allow running [cmp.Diff] at the time of failure message generation.
func BeMatchingK8sResource[T any](expected T, comparator func(T, T) bool) types.GomegaMatcher {
	return &beMatchingK8sResource[T]{
		expected:   expected,
		comparator: comparator,
	}
}

type beMatchingK8sResource[T any] struct {
	expected   T
	comparator func(r1 T, r2 T) bool
}

var _ types.GomegaMatcher = &beMatchingK8sResource[any]{}

func (m *beMatchingK8sResource[T]) Match(actual interface{}) (success bool, err error) {
	actualT, ok := actual.(T)
	if !ok {
		return false, fmt.Errorf("BeMatchingK8sResource matcher expects two objects of the same type")
	}

	return m.comparator(m.expected, actualT), nil
}

func (m *beMatchingK8sResource[T]) FailureMessage(actual interface{}) (message string) {
	diff := cmp.Diff(actual, m.expected)
	return fmt.Sprintf("Expected\n\t%#v\nto compare identical to\n\t%#v\nbut it differs in\n%s", actual, m.expected, diff)
}

func (m *beMatchingK8sResource[T]) NegatedFailureMessage(actual interface{}) (message string) {
	diff := cmp.Diff(actual, m.expected)
	return fmt.Sprintf("Expected\n\t%#v\nto not compare identical to\n\t%#v\nit differs in\n%s", actual, m.expected, diff)
}
