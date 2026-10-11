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

package ide

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/programstatus"
)

const cmdStatusList = "statuslist"

// detachedStatusLabel names a terminal that is no tab and whose program
// did not name itself.
const detachedStatusLabel = "terminal"

// statusHost is what a statusStore needs from the workspaces whose
// statuses it accounts for. It is only called on the host event loop.
type statusHost interface {
	// statusWorkspace returns the slot of the open workspace at ws and
	// the name it is shown under, or false when no workspace at ws
	// holds a slot.
	statusWorkspace(ws workspaceapi.URI) (slot int, name string, ok bool)
	// statusTabName returns the name of the tab at uri in the
	// workspace at ws, or false when there is no such tab.
	statusTabName(ws, uri workspaceapi.URI) (string, bool)
	// statusInSight reports whether the user sees what is at uri in
	// the workspace at ws: the tab in focus, or any terminal that is
	// no tab, as it floats over its workspace, while the workspace is
	// in focus.
	statusInSight(ws, uri workspaceapi.URI) bool
	// notifyStatus tells the user msg at level in the workspace at ws,
	// if it is still open.
	notifyStatus(ws workspaceapi.URI, level browserapi.NotificationLevel, msg string)
	// statusChanged tells that the status reported somewhere in the
	// workspace at ws changed.
	statusChanged(ws workspaceapi.URI)
	// showStatus brings what is at uri in the workspace at ws in
	// sight, or tells the user why it cannot.
	showStatus(ws, uri workspaceapi.URI)
}

// tabStatusNotifyConfig says which states a source entering raises a
// notification for while out of sight, the minimum time between two
// such notifications for the same source, and whether the messages
// programs report are notified at all.
type tabStatusNotifyConfig struct {
	states   map[programstatus.State]bool
	interval time.Duration
	messages bool
}

func defaultTabStatusNotifyConfig() tabStatusNotifyConfig {
	return tabStatusNotifyConfig{
		states: map[programstatus.State]bool{
			programstatus.Blocked: true,
			programstatus.Error:   true,
			programstatus.Done:    true,
		},
		interval: 2 * time.Second,
		messages: true,
	}
}

// tabStatusConfigFor returns the tab status styling cfg asks for, or
// no styling at all when terminal.status is disabled.
func tabStatusConfigFor(cfg ideConfig) browser.TabStatusConfig {
	if !cfg.tabStatusEnabled() {
		return browser.TabStatusConfig{}
	}
	return cfg.tabStatusConfig()
}

// statusKey identifies a status source: what is at uri in the
// workspace at ws.
type statusKey struct {
	ws, uri string
}

// statusSource is what reported a status: a tab, or a terminal that is
// no tab, such as a floating "!" command or a task.
type statusSource struct {
	ws, uri workspaceapi.URI
	// records holds what a terminal that is no tab reported. It is nil
	// for a tab, whose records its browser holds.
	records *programstatus.Store
	status  programstatus.Summary
}

func (src *statusSource) key() statusKey {
	return statusKey{ws: src.ws.String(), uri: src.uri.String()}
}

// statusStore accounts for every program status reported across the
// workspaces, whether by the program behind a tab or by a terminal that
// is no tab. It rolls them up per workspace, tells the user about the
// transitions worth knowing, and lists them, live, in the status
// inbox. A source is forgotten once it holds no record, and with its
// workspace. It is not safe for concurrent use: every method must be
// called on the host event loop. Use newStatusStore to create one.
type statusStore struct {
	host     statusHost
	cfg      browser.TabStatusConfig
	notify   tabStatusNotifyConfig
	schedule func(func()) bool
	sources  map[statusKey]*statusSource
	// lastNotified and pendingNotify rate limit the notifications of
	// each source: a pending entry holds the sequence number of the
	// deferred notification that may still deliver it.
	lastNotified  map[statusKey]time.Time
	pendingNotify map[statusKey]uint64
	notifySeq     uint64
	views         map[*statusView]struct{}
}

// newStatusStore returns an empty store that styles statuses after cfg
// and notifies them as notify says. schedule runs a function on the
// next tick of the host event loop.
func newStatusStore(
	host statusHost, cfg browser.TabStatusConfig, notify tabStatusNotifyConfig,
	schedule func(func()) bool,
) *statusStore {
	return &statusStore{
		host:          host,
		cfg:           cfg,
		notify:        notify,
		schedule:      schedule,
		sources:       make(map[statusKey]*statusSource),
		lastNotified:  make(map[statusKey]time.Time),
		pendingNotify: make(map[statusKey]uint64),
		views:         make(map[*statusView]struct{}),
	}
}

// setTabStatus applies report, made by the program behind uri in the
// workspace at ws, to the tab at uri among tabs, the workspace's tabs,
// or, when uri is no tab, to the records the store keeps for it.
func (s *statusStore) setTabStatus(
	ws workspaceapi.URI, tabs browser.TabManager, uri workspaceapi.URI,
	report programstatus.Report,
) error {
	if _, ok := s.host.statusTabName(ws, uri); ok {
		return tabs.SetTabStatus(uri, report)
	}
	s.updateDetached(ws, uri, func(r *programstatus.Store) { r.Apply(report) })
	return nil
}

// endTabStatus tells that the program behind uri in the workspace at ws
// ended, as setTabStatus applies a report.
func (s *statusStore) endTabStatus(
	ws workspaceapi.URI, tabs browser.TabManager, uri workspaceapi.URI,
) error {
	if _, ok := s.host.statusTabName(ws, uri); ok {
		return tabs.EndTabStatus(uri)
	}
	s.updateDetached(ws, uri, func(r *programstatus.Store) { r.DropTransient() })
	return nil
}

// tabStatusChanged accounts for the status of the tab at uri in the
// workspace at ws becoming status. Every change of a tab's status must
// be reported here, as it is the only way the store learns about tabs.
func (s *statusStore) tabStatusChanged(ws, uri workspaceapi.URI, status programstatus.Summary) {
	src, ok := s.sources[statusKey{ws: ws.String(), uri: uri.String()}]
	if !ok {
		src = &statusSource{ws: ws, uri: uri, status: programstatus.Summary{Progress: -1}}
	}
	wasDetached := src.records != nil
	src.records = nil
	s.update(src, status)
	if wasDetached {
		s.refreshViews()
	}
}

// dropWorkspace forgets every status reported in the workspace at ws.
func (s *statusStore) dropWorkspace(ws workspaceapi.URI) {
	dropped := false
	for key := range s.sources {
		if key.ws == ws.String() {
			delete(s.sources, key)
			dropped = true
		}
	}
	for key := range s.lastNotified {
		if key.ws == ws.String() {
			delete(s.lastNotified, key)
		}
	}
	for key := range s.pendingNotify {
		if key.ws == ws.String() {
			delete(s.pendingNotify, key)
		}
	}
	if dropped {
		s.host.statusChanged(ws)
		s.refreshViews()
	}
}

// status returns the status last reported at uri in the workspace at
// ws, and false when nothing there holds a record.
func (s *statusStore) status(ws, uri workspaceapi.URI) (programstatus.Summary, bool) {
	src, ok := s.sources[statusKey{ws: ws.String(), uri: uri.String()}]
	if !ok {
		return programstatus.Summary{}, false
	}
	return src.status, true
}

// rollup returns the most pressing status reported in the workspace at
// ws (blocked > error > done > working > reported idle) and the uri it
// was reported at, the first in uri order on a tie. When nothing was
// reported it returns an empty idle status and a zero uri.
func (s *statusStore) rollup(ws workspaceapi.URI) (programstatus.Summary, workspaceapi.URI) {
	sum, uri := programstatus.Summary{Progress: -1}, workspaceapi.URI{}
	for _, src := range s.sorted() {
		if src.ws.String() == ws.String() && src.status.Outranks(sum) {
			sum, uri = src.status, src.uri
		}
	}
	return sum, uri
}

// items lists every source of a workspace that holds a slot, sorted by
// workspace and uri.
func (s *statusStore) items() []statusItem {
	var ret []statusItem
	for _, src := range s.sorted() {
		slot, wsName, ok := s.host.statusWorkspace(src.ws)
		if !ok {
			continue
		}
		ret = append(ret, statusItem{
			slot: slot, ws: src.ws, wsName: wsName,
			uri: src.uri, name: s.name(src), status: src.status,
		})
	}
	return ret
}

// view returns a new live status inbox over the store.
func (s *statusStore) view() *statusView {
	v := &statusView{store: s, list: component.NewFocusList()}
	v.set(statusInbox{cfg: s.cfg}.entries(s.items()))
	s.views[v] = struct{}{}
	return v
}

func (s *statusStore) sorted() []*statusSource {
	ret := slices.Collect(maps.Values(s.sources))
	slices.SortFunc(ret, func(a, b *statusSource) int {
		return cmp.Or(
			strings.Compare(a.ws.String(), b.ws.String()),
			strings.Compare(a.uri.String(), b.uri.String()))
	})
	return ret
}

// name returns the name of the tab src is, or detachedStatusLabel when
// it is no tab.
func (s *statusStore) name(src *statusSource) string {
	if src.records != nil {
		return detachedStatusLabel
	}
	name, _ := s.host.statusTabName(src.ws, src.uri)
	return name
}

// updateDetached applies fn to the records kept for uri in the
// workspace at ws, which is no tab.
func (s *statusStore) updateDetached(
	ws, uri workspaceapi.URI, fn func(*programstatus.Store),
) {
	src, ok := s.sources[statusKey{ws: ws.String(), uri: uri.String()}]
	if !ok || src.records == nil {
		src = &statusSource{
			ws: ws, uri: uri, records: programstatus.New(),
			status: programstatus.Summary{Progress: -1},
		}
	}
	fn(src.records)
	s.update(src, src.records.Summary())
}

// update sets the status of src, keeping src while it holds a record,
// and reacts to the change.
func (s *statusStore) update(src *statusSource, status programstatus.Summary) {
	if src.status.Equal(status) {
		return
	}
	prev := src.status
	src.status = status
	if len(status.Records) == 0 {
		delete(s.sources, src.key())
	} else {
		s.sources[src.key()] = src
	}
	s.host.statusChanged(src.ws)
	s.announce(src, prev)
	s.refreshViews()
}

// announce notifies what changed in src since prev:
//   - every new message a record carries, at once and whether or not
//     src is in sight, since nothing else shows it;
//   - src entering a state worth telling the user about while out of
//     sight, unless a message in that state already did, at most once
//     per source every notify interval with the latest state winning.
func (s *statusStore) announce(src *statusSource, prev programstatus.Summary) {
	key, status := src.key(), src.status
	announced := false
	if s.notify.messages {
		for _, rec := range newStatusMessages(prev, status) {
			s.host.notifyStatus(src.ws, statusNotificationLevel(rec.State),
				statusSourceLabel(s.name(src), rec.App)+": "+rec.Msg)
			announced = announced || rec.State == status.State
		}
	}
	if announced {
		delete(s.pendingNotify, key)
	}
	if prev.State == status.State || !s.notify.states[status.State] {
		return
	}
	if announced {
		s.lastNotified[key] = time.Now()
		return
	}
	if _, pending := s.pendingNotify[key]; pending {
		return
	}
	wait := s.notify.interval - time.Since(s.lastNotified[key])
	if wait <= 0 {
		s.notifyState(key)
		return
	}
	s.notifySeq++
	seq := s.notifySeq
	s.pendingNotify[key] = seq
	time.AfterFunc(wait, func() {
		debug.CapturePanicReport(func() {
			_ = s.schedule(func() {
				if pending, ok := s.pendingNotify[key]; !ok || pending != seq {
					return
				}
				delete(s.pendingNotify, key)
				s.notifyState(key)
			})
		})
	})
}

// notifyState notifies the current state of the source at key, unless
// it is gone, has since left the states worth notifying, or is in
// sight. Messages are notified on their own, so this names the state
// only.
func (s *statusStore) notifyState(key statusKey) {
	src, ok := s.sources[key]
	if !ok || !s.notify.states[src.status.State] || s.host.statusInSight(src.ws, src.uri) {
		return
	}
	s.lastNotified[key] = time.Now()
	status := src.status
	text := status.State.String()
	if status.State == programstatus.Blocked && status.Kind != programstatus.KindNone {
		text += " (" + status.Kind.String() + ")"
	}
	if status.Title != "" {
		text = status.Title + " — " + text
	}
	s.host.notifyStatus(src.ws, statusNotificationLevel(status.State),
		statusSourceLabel(s.name(src), status.App)+": "+text)
}

func (s *statusStore) refreshViews() {
	if len(s.views) == 0 {
		return
	}
	entries := statusInbox{cfg: s.cfg}.entries(s.items())
	for v := range s.views {
		v.set(entries)
	}
}

// newStatusMessages returns the records of status whose message is
// new: absent from the record with the same id in prev, or different
// there.
func newStatusMessages(prev, status programstatus.Summary) []programstatus.Record {
	var ret []programstatus.Record
	for _, rec := range status.Records {
		if rec.Msg == "" {
			continue
		}
		i := slices.IndexFunc(prev.Records, func(p programstatus.Record) bool {
			return p.ID == rec.ID
		})
		if i < 0 || prev.Records[i].Msg != rec.Msg {
			ret = append(ret, rec)
		}
	}
	return ret
}

func statusNotificationLevel(state programstatus.State) browserapi.NotificationLevel {
	switch state {
	case programstatus.Blocked:
		return browserapi.LevelWarn
	case programstatus.Error:
		return browserapi.LevelError
	case programstatus.Done:
		return browserapi.LevelSuccess
	}
	return browserapi.LevelInfo
}

// statusSourceLabel names the source of a status for the user: the
// tab's name followed by the app that reported it when that adds to
// the name, or the app alone for a terminal that is no tab.
func statusSourceLabel(name, app string) string {
	switch {
	case app == "" || app == name:
		return name
	case name == "" || name == detachedStatusLabel:
		return app
	}
	return name + " (" + app + ")"
}

// statusItem is one source as the status inbox lists it.
type statusItem struct {
	slot int
	ws   workspaceapi.URI
	// wsName is the workspace's display name.
	wsName string
	uri    workspaceapi.URI
	// name is the tab's name, or detachedStatusLabel.
	name   string
	status programstatus.Summary
}

// statusEntry is one row of the status inbox: a source, or one of the
// records beneath it.
type statusEntry struct {
	ws, uri workspaceapi.URI
	// record is the id of the record the row shows, empty for the row
	// of the source itself.
	record  string
	state   programstatus.State
	display string
	attr    term.Attributes
}

func (e statusEntry) same(o statusEntry) bool {
	return e.ws.String() == o.ws.String() && e.uri.String() == o.uri.String() &&
		e.record == o.record
}

// statusInbox renders status items as inbox rows, styled after the tab
// status configuration.
type statusInbox struct {
	cfg browser.TabStatusConfig
}

// statusInboxMaxDepth caps the indentation of a record's row.
const statusInboxMaxDepth = 4

// entries returns one row per non-idle item, most pressing first
// (blocked > error > done > working), then by slot and name, followed
// by one indented row per record beneath it when an item holds more
// than one.
func (in statusInbox) entries(items []statusItem) []statusEntry {
	items = slices.DeleteFunc(slices.Clone(items), func(it statusItem) bool {
		return it.status.State == programstatus.Idle
	})
	slices.SortStableFunc(items, func(a, b statusItem) int {
		return cmp.Or(
			programstatus.Precedence(b.status.State)-programstatus.Precedence(a.status.State),
			a.slot-b.slot,
			strings.Compare(a.name, b.name))
	})
	var ret []statusEntry
	for _, it := range items {
		ret = append(ret, statusEntry{
			ws:    it.ws,
			uri:   it.uri,
			state: it.status.State,
			display: in.row(it.status.State, it.status.Kind, 0,
				it.wsName, statusSourceLabel(it.name, it.status.App),
				it.status.Title, it.status.Progress, it.status.Msg),
			attr: in.rowAttr(it.status.State),
		})
		if len(it.status.Records) < 2 {
			continue
		}
		for _, rec := range it.status.Records {
			if rec.ID == "" {
				continue
			}
			depth := min(strings.Count(rec.ID, "/")+1, statusInboxMaxDepth)
			label := rec.ID[strings.LastIndexByte(rec.ID, '/')+1:]
			ret = append(ret, statusEntry{
				ws:     it.ws,
				uri:    it.uri,
				record: rec.ID,
				state:  rec.State,
				display: in.row(rec.State, rec.Kind, depth,
					"", label, rec.Title, rec.Progress, rec.Msg),
				attr: in.rowAttr(rec.State),
			})
		}
	}
	return ret
}

// row renders one inbox row: the state's glyph indented by depth, then
// the workspace, label, title, progress and message, skipping the empty
// ones. Progress is shown for working rows only.
func (in statusInbox) row(
	state programstatus.State, kind programstatus.Kind, depth int,
	ws, label, title string, progress int, msg string,
) string {
	fields := []string{strings.Repeat("  ", depth) + in.glyph(state, kind)}
	for _, f := range []string{ws, label, title} {
		if f != "" {
			fields = append(fields, f)
		}
	}
	if state == programstatus.Working && progress >= 0 {
		fields = append(fields, strconv.Itoa(progress)+"%")
	}
	if msg != "" {
		fields = append(fields, msg)
	}
	return strings.Join(fields, "  ")
}

// glyph returns the icon configured for state, the first spinner frame
// for working, or the state's name when none is configured.
func (in statusInbox) glyph(state programstatus.State, kind programstatus.Kind) string {
	var icon rune
	switch state {
	case programstatus.Working:
		if len(in.cfg.Spinner) > 0 {
			icon = in.cfg.Spinner[0]
		}
	case programstatus.Blocked:
		icon = in.cfg.BlockedIcons[kind]
		if icon == 0 {
			icon = in.cfg.BlockedIcons[programstatus.KindNone]
		}
	case programstatus.Done:
		icon = in.cfg.DoneIcon
	case programstatus.Error:
		icon = in.cfg.ErrorIcon
	}
	if icon == 0 {
		return state.String()
	}
	return string(icon)
}

// rowAttr returns the attributes a row in state is drawn with: the
// state's name attributes, or its icon attributes when it has none.
func (in statusInbox) rowAttr(state programstatus.State) term.Attributes {
	name, icon := in.cfg.BarAttrs(programstatus.Summary{State: state})
	if name == (term.Attributes{}) {
		return icon
	}
	return name
}

const (
	statusViewEmpty     = "No program reported a status"
	statusViewMinWidth  = 40
	statusViewMaxHeight = 15
)

// statusView is the status inbox: a floating list of every status the
// store accounts for, kept up to date until it is closed. Selecting a
// row brings its source in sight and closes the view.
type statusView struct {
	store   *statusStore
	entries []statusEntry
	list    *component.FocusList
	idealW  int
	idealH  int
	width   int
	height  int
}

// set replaces the rows with entries, keeping the focus on the row of
// the same source or record when it is still listed.
func (v *statusView) set(entries []statusEntry) {
	focus := 0
	if idx := v.list.FocusOffset(); idx >= 0 && idx < len(v.entries) {
		if i := slices.IndexFunc(entries, v.entries[idx].same); i >= 0 {
			focus = i
		} else {
			focus = min(idx, max(len(entries)-1, 0))
		}
	}
	v.entries = entries
	v.list.Reset()
	maxW, rows := 0, len(entries)
	if rows == 0 {
		v.list.PushBack(component.NewResponsiveString(statusViewEmpty,
			component.StringResponsiveConfig{}))
		maxW, rows = graphemecluster.StringWidth(statusViewEmpty), 1
	}
	for _, e := range entries {
		v.list.PushBack(component.NewResponsiveString(e.display,
			component.StringResponsiveConfig{
				StringConfig: component.StringConfig{Attributes: e.attr},
			}))
		maxW = max(maxW, graphemecluster.StringWidth(e.display))
	}
	v.list.Resize(v.width, v.height)
	v.list.FocusStart()
	for range focus {
		v.list.FocusDown()
	}
	v.idealW = max(maxW, statusViewMinWidth)
	v.idealH = min(rows, statusViewMaxHeight)
}

func (v *statusView) Resize(width, height int) {
	v.width, v.height = width, height
	v.list.Resize(width, height)
}

func (v *statusView) Draw(w term.Writer) {
	v.list.Draw(w)
}

func (v *statusView) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		return false, false
	}
	switch ev.Key {
	case term.KeyEsc:
		return true, true
	case term.KeyEnter:
		if idx := v.list.FocusOffset(); idx >= 0 && idx < len(v.entries) {
			entry := v.entries[idx]
			// The view's window closes once Handle returns, so the
			// source is shown on the next tick to leave the window with
			// the workspace that opened it.
			_ = v.store.schedule(func() { v.store.host.showStatus(entry.ws, entry.uri) })
		}
		return true, true
	case term.KeyArrowUp:
		v.list.FocusUp()
		return false, true
	case term.KeyArrowDown:
		v.list.FocusDown()
		return false, true
	}
	if ev.Mod == term.ModCtrl {
		switch ev.Ch {
		case 'j', 'n':
			v.list.FocusDown()
			return false, true
		case 'k', 'p':
			v.list.FocusUp()
			return false, true
		case 'c':
			return true, true
		}
	}
	switch ev.Ch {
	case 'j':
		v.list.FocusDown()
		return false, true
	case 'k':
		v.list.FocusUp()
		return false, true
	case 'q':
		return true, true
	}
	return false, false
}

func (v *statusView) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func (v *statusView) Selection() (string, bool) {
	idx := v.list.FocusOffset()
	if idx < 0 || idx >= len(v.entries) {
		return "", false
	}
	return v.entries[idx].display, true
}

// Close stops following the store.
func (v *statusView) Close() error {
	delete(v.store.views, v)
	return nil
}

func (v *statusView) Dimensions() (int, int) {
	return v.idealW, v.idealH
}
