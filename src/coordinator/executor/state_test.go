// Copyright (c) 2018 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package executor

import (
	"context"
	"testing"

	"github.com/m3db/m3coordinator/functions"
	"github.com/m3db/m3coordinator/parser"
	"github.com/m3db/m3coordinator/plan"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidState(t *testing.T) {
	fetchTransform := parser.NewTransformFromOperation(functions.FetchOp{}, 1)
	countTransform := parser.NewTransformFromOperation(functions.CountOp{}, 2)
	transforms := parser.Nodes{fetchTransform, countTransform}
	edges := parser.Edges{
		parser.Edge{
			ParentID: fetchTransform.ID,
			ChildID:  countTransform.ID,
		},
	}

	lp, err := plan.NewLogicalPlan(transforms, edges)
	require.NoError(t, err)
	p, err := plan.NewPhysicalPlan(lp, nil)
	require.NoError(t, err)
	state, err := GenerateExecutionState(p, nil)
	require.NoError(t, err)
	require.Len(t, state.sources, 1)
	err = state.Execute(context.Background())
	assert.Error(t, err, "not implemented")
}

func TestWithoutSources(t *testing.T) {
	countTransform := parser.NewTransformFromOperation(functions.CountOp{}, 2)
	transforms := parser.Nodes{countTransform}
	edges := parser.Edges{}
	lp, err := plan.NewLogicalPlan(transforms, edges)
	require.NoError(t, err)
	p, err := plan.NewPhysicalPlan(lp, nil)
	require.NoError(t, err)
	_, err = GenerateExecutionState(p, nil)
	assert.Error(t, err)
}

func TestOnlySources(t *testing.T) {
	fetchTransform := parser.NewTransformFromOperation(functions.FetchOp{}, 1)
	transforms := parser.Nodes{fetchTransform}
	edges := parser.Edges{}
	lp, err := plan.NewLogicalPlan(transforms, edges)
	require.NoError(t, err)
	p, err := plan.NewPhysicalPlan(lp, nil)
	require.NoError(t, err)
	state, err := GenerateExecutionState(p, nil)
	assert.NoError(t, err)
	require.Len(t, state.sources, 1)
}

func TestMultipleSources(t *testing.T) {
	fetchTransform1 := parser.NewTransformFromOperation(functions.FetchOp{}, 1)
	countTransform := parser.NewTransformFromOperation(functions.CountOp{}, 2)
	fetchTransform2 := parser.NewTransformFromOperation(functions.FetchOp{}, 3)
	transforms := parser.Nodes{fetchTransform1, fetchTransform2, countTransform}
	edges := parser.Edges{
		parser.Edge{
			ParentID: fetchTransform1.ID,
			ChildID:  countTransform.ID,
		},
		parser.Edge{
			ParentID: fetchTransform2.ID,
			ChildID:  countTransform.ID,
		},
	}

	lp, err := plan.NewLogicalPlan(transforms, edges)
	require.NoError(t, err)
	p, err := plan.NewPhysicalPlan(lp, nil)
	require.NoError(t, err)
	state, err := GenerateExecutionState(p, nil)
	assert.NoError(t, err)
	require.Len(t, state.sources, 2)
	assert.Contains(t, state.String(), "sources")
}