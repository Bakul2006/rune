// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package memory

import (
	"context"
	"errors"
	"log/slog"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/configedit"
)

// memoryRecallEnabledKey is the configedit key consulted by the
// GuardedRecaller. true = always allow memory recall; false = always
// skip memory recall; absent = ask the user the first time a recall
// would surface at least one memory.
const memoryRecallEnabledKey = "memory_recall_enabled"

// GuardedRecaller wraps an inner agent.MemoryRecaller with a
// configurable on/off switch and a one-time prompt that records the
// user's preference.
//
// Behavior on each Recall call:
//   - If memory_recall_enabled is true: delegate to the inner recaller.
//   - If memory_recall_enabled is false: return (nil, nil) without
//     calling the inner recaller.
//   - If absent: call the inner recaller; if it returns no memories
//     or an error, return as-is without prompting. Otherwise prompt
//     the user with Yes / Always / No / Never. Always/Never are
//     persisted to .rune/config.yaml; Yes/No are one-shot decisions.
//
// enabled is the deferred configedit.Bool view of memory_recall_enabled.
// It is built once at construction time and resolved per call so writes
// through cfg.SetBool (Always/Never choices) are observed immediately in
// the same session.
type GuardedRecaller struct {
	inner   agent.MemoryRecaller
	cfg     configedit.Config
	enabled configedit.Bool
}

// NewGuardedRecaller returns a GuardedRecaller wired to inner and cfg.
// Panics if inner or cfg is nil.
func NewGuardedRecaller(
	inner agent.MemoryRecaller, cfg configedit.Config,
) *GuardedRecaller {
	if inner == nil {
		panic("memory: GuardedRecaller inner must not be nil")
	}
	if cfg == nil {
		panic("memory: GuardedRecaller cfg must not be nil")
	}
	return &GuardedRecaller{
		inner:   inner,
		cfg:     cfg,
		enabled: cfg.GetBool(memoryRecallEnabledKey),
	}
}

// Recall implements agent.MemoryRecaller.
func (g *GuardedRecaller) Recall(
	ctx context.Context, files []string, task string, workspaceRoot string,
) ([]agent.Memory, error) {
	v, err := g.enabled.Resolve(ctx)
	switch {
	case err == nil:
		if v {
			return g.inner.Recall(ctx, files, task, workspaceRoot)
		}
		return nil, nil
	case errors.Is(err, configedit.ErrNotFound):
		// fall through to prompt
	default:
		slog.Warn("get memory_recall_enabled from config", "error", err)
		// fall through; default to attempting recall
	}

	mems, err := g.inner.Recall(ctx, files, task, workspaceRoot)
	if err != nil || len(mems) == 0 {
		return mems, err
	}

	prompter := agent.PrompterFromContext(ctx)
	if prompter == nil {
		// No prompter configured (tests, e2e tools): default-allow so
		// memories are not silently dropped.
		return mems, nil
	}

	resp, err := prompter.Prompt(ctx, agent.PromptRequest{
		Title:  "Allow memory recall?",
		Header: "memory",
		Kind:   agent.PromptKindPermission,
		Body: "Rune Agent has compiled memories from past sessions in " +
			"this workspace. Memories surface architecture decisions, " +
			"user preferences, and episodic context that may be " +
			"relevant to your request. Include them in this turn?",
		Options: []agent.PromptOption{
			{Value: "yes", Label: "Yes",
				Description: "Use memories for this call"},
			{Value: "always", Label: "Always",
				Description: "Always use memories; remember the choice"},
			{Value: "no", Label: "No",
				Description: "Skip memories for this call"},
			{Value: "never", Label: "Never",
				Description: "Always skip memories; remember the choice"},
		},
	})
	if err != nil {
		// Dismissed: treat as "no" for this call; do not persist.
		return nil, nil
	}
	var choice string
	if len(resp.Values) > 0 {
		choice = resp.Values[0]
	}
	switch choice {
	case "yes":
		return mems, nil
	case "always":
		if err := setMemoryRecallEnabled(ctx, g.cfg, true, false); err != nil {
			slog.Warn("persist memory_recall_enabled=true", "error", err)
		}
		return mems, nil
	case "no":
		return nil, nil
	case "never":
		if err := setMemoryRecallEnabled(ctx, g.cfg, false, false); err != nil {
			slog.Warn("persist memory_recall_enabled=false", "error", err)
		}
		return nil, nil
	default:
		// Unknown response: treat as "no" for this call.
		return nil, nil
	}
}

// setMemoryRecallEnabled updates memory_recall_enabled via cfg.
// When ephemeral is false the value is persisted to .rune/config.yaml;
// when true it is only written to the in-memory overlay and lost on
// restart. Subsequent Resolve calls on the corresponding
// configedit.Bool observe the new value either way.
func setMemoryRecallEnabled(
	ctx context.Context, cfg configedit.Setter, v, ephemeral bool,
) error {
	return cfg.SetBool(ctx, memoryRecallEnabledKey, v, ephemeral)
}
