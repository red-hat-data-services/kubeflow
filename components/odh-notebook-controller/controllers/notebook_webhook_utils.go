/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"github.com/google/go-cmp/cmp"

	"github.com/go-logr/logr"
)

// UpdatesPending is either NoPendingUpdates, or a new value providing a Reason for the update.
type UpdatesPending struct {
	Reason string
}

var (
	NoPendingUpdates = &UpdatesPending{}
)

// FirstDifferenceReporter is a custom go-cmp reporter that only records the first difference.
type FirstDifferenceReporter struct {
	path cmp.Path
	diff string
}

func (r *FirstDifferenceReporter) PushStep(ps cmp.PathStep) {
	r.path = append(r.path, ps)
}

func (r *FirstDifferenceReporter) Report(rs cmp.Result) {
	if r.diff == "" && !rs.Equal() {
		vx, vy := r.path.Last().Values()
		r.diff = fmt.Sprintf("%#v: %+v != %+v", r.path, vx, vy)
	}
}

func (r *FirstDifferenceReporter) PopStep() {
	r.path = r.path[:len(r.path)-1]
}

func (r *FirstDifferenceReporter) String() string {
	return r.diff
}

// getStructDiff compares a and b, reporting the first difference it found in a human-readable single-line string.
func getStructDiff(ctx context.Context, a any, b any) (result string) {
	log := logr.FromContextOrDiscard(ctx)

	// calling cmp.Equal may panic, get ready for it
	result = "failed to compute the reason for why there is a pending restart"
	defer func() {
		if r := recover(); r != nil {
			log.Error(fmt.Errorf("failed to compute struct difference: %+v", r), "Cannot determine reason for restart")
		}
	}()

	var comparator FirstDifferenceReporter
	eq := cmp.Equal(a, b, cmp.Reporter(&comparator))
	if eq {
		log.Error(nil, "Unexpectedly attempted to diff structs that are actually equal")
	}
	result = comparator.String()

	return
}
