/*
Copyright 2021 Gravitational, Inc.

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

package bot

import (
	"context"
	"testing"

	"github.com/gravitational/teleport/.github/workflows/robot/internal/env"
	"github.com/gravitational/teleport/.github/workflows/robot/internal/github"

	"github.com/stretchr/testify/require"
)

// TestLabel checks that labels are correctly applied to a Pull Request.
func TestLabel(t *testing.T) {
	tests := []struct {
		desc   string
		branch string
		files  []github.PullRequestFile
		labels []string
	}{
		{
			desc:   "code-only",
			branch: "foo",
			files: []github.PullRequestFile{
				{Name: "file.go"},
				{Name: "examples/README.md"},
			},
			labels: []string{},
		},
		{
			desc:   "docs",
			branch: "foo",
			files: []github.PullRequestFile{
				{Name: "docs/docs.md"},
			},
			labels: []string{"documentation"},
		},
		{
			desc:   "helm",
			branch: "foo",
			files: []github.PullRequestFile{
				{Name: "examples/chart/index.html"},
			},
			labels: []string{"helm"},
		},
		{
			desc:   "docs-and-helm",
			branch: "foo",
			files: []github.PullRequestFile{
				{Name: "docs/docs.md"},
				{Name: "examples/chart/index.html"},
			},
			labels: []string{"documentation", "helm"},
		},
		{
			desc:   "docs-and-backport",
			branch: "branch/foo",
			files: []github.PullRequestFile{
				{Name: "docs/docs.md"},
			},
			labels: []string{"backport", "documentation"},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			b := &Bot{
				c: &Config{
					Environment: &env.Environment{
						Organization: "foo",
						Repository:   "bar",
						Number:       0,
						UnsafeHead:   test.branch,
					},
				},
			}
			labels, err := b.labels(context.Background(), test.files)
			require.NoError(t, err)
			require.ElementsMatch(t, labels, test.labels)
		})
	}
}
