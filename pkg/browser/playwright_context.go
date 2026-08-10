package browser

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	playwrightContextMarker        = "MINTCLAW_CONTEXT_V1"
	playwrightContextResponseBytes = MaxContextCatalogBytes * 2
)

var playwrightPrivateContextToken = regexp.MustCompile(`^[pf][1-9][0-9]{0,9}$`)

// playwrightContextProbeCode installs a worker-private lifecycle registry on
// the BrowserContext. Its raw tokens and tab indexes never leave this adapter.
const playwrightContextProbeCode = `async (page) => {
  const key = Symbol.for("mintclaw.browser.context-registry.v1");
  const context = page.context();
  let state = context[key];
  if (!state) {
    state = { nextPage: 1, nextFrame: 1, generation: 1,
      pages: new WeakMap(), frames: new WeakMap() };
    Object.defineProperty(context, key, { value: state, configurable: true });
    const registerFrame = frame => {
      let record = state.frames.get(frame);
      if (!record) {
        record = { token: "f" + state.nextFrame++, generation: 1 };
        state.frames.set(frame, record);
        frame.on("navigated", () => { record.generation++; state.generation++; });
      }
      return record;
    };
    const registerPage = candidate => {
      let record = state.pages.get(candidate);
      if (!record) {
        record = { token: "p" + state.nextPage++ };
        state.pages.set(candidate, record);
        state.generation++;
        candidate.on("frameattached", frame => { registerFrame(frame); state.generation++; });
        candidate.on("framedetached", () => { state.generation++; });
        candidate.on("close", () => { state.generation++; });
      }
      registerFrame(candidate.mainFrame());
      return record;
    };
    state.registerFrame = registerFrame;
    state.registerPage = registerPage;
    for (const candidate of context.pages()) registerPage(candidate);
    context.on("page", registerPage);
  }
  const pages = context.pages();
  const result = [];
  for (let index = 0; index < pages.length; index++) {
    const candidate = pages[index];
    const pageRecord = state.registerPage(candidate);
    const opener = await candidate.opener();
    const openerToken = opener ? state.registerPage(opener).token : "";
    const frames = [];
    for (const frame of candidate.frames()) {
      if (frame === candidate.mainFrame()) continue;
      const frameRecord = state.registerFrame(frame);
      const parent = frame.parentFrame();
      frames.push({ token: frameRecord.token,
        parent: parent && parent !== candidate.mainFrame() ? state.registerFrame(parent).token : "",
        generation: frameRecord.generation, url: frame.url(), label: frame.name() || "" });
    }
    const mainRecord = state.registerFrame(candidate.mainFrame());
    result.push({ token: pageRecord.token, index, opener: openerToken,
      generation: mainRecord.generation, url: candidate.url(), title: await candidate.title(), frames });
  }
  return "MINTCLAW_CONTEXT_V1|ok|" + encodeURIComponent(JSON.stringify({
    generation: state.generation, selected: state.registerPage(page).token, pages: result
  }));
}`

type playwrightRawContextCatalog struct {
	Generation uint64              `json:"generation"`
	Selected   string              `json:"selected"`
	Pages      []playwrightRawPage `json:"pages"`
}

type playwrightRawPage struct {
	Token      string               `json:"token"`
	Index      int                  `json:"index"`
	Opener     string               `json:"opener"`
	Generation uint64               `json:"generation"`
	URL        string               `json:"url"`
	Title      string               `json:"title"`
	Frames     []playwrightRawFrame `json:"frames"`
}

type playwrightRawFrame struct {
	Token      string `json:"token"`
	Parent     string `json:"parent"`
	Generation uint64 `json:"generation"`
	URL        string `json:"url"`
	Label      string `json:"label"`
}

type playwrightContextState struct {
	catalogID     string
	generation    uint64
	rawGeneration uint64
	creation      uint64
	selectedTabID string
	selectedFrame string
	tabs          map[string]string
	frames        map[string]string
	tabSequence   map[string]uint64
	frameSequence map[string]uint64
	tabIndexes    map[string]int
}

func (worker *playwrightWorker) ContextCatalog(ctx context.Context) (ContextCatalog, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.contextCatalogLocked(ctx)
}

func (worker *playwrightWorker) OpenTab(ctx context.Context) (ContextCatalog, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if err := worker.contextAvailableLocked(); err != nil {
		return ContextCatalog{}, err
	}
	current, _, err := worker.probeContextsLocked(ctx)
	if err != nil {
		return ContextCatalog{}, err
	}
	maximumTabs := worker.limits.Tabs
	if maximumTabs < 1 || maximumTabs > MaxContextTabs {
		maximumTabs = MaxContextTabs
	}
	if len(current.Tabs) >= maximumTabs {
		return ContextCatalog{}, ErrDenied
	}
	if _, err = worker.callAndConsume(ctx, "browser_tabs", map[string]any{
		"action": "new",
	}, true); err != nil {
		return ContextCatalog{}, err
	}
	return worker.contextCatalogLocked(ctx)
}

func (worker *playwrightWorker) SelectContext(
	ctx context.Context,
	tabID string,
	frameID string,
) (DriverObservation, ContextCatalog, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if err := worker.contextAvailableLocked(); err != nil {
		return DriverObservation{}, ContextCatalog{}, err
	}
	catalog, raw, err := worker.probeContextsLocked(ctx)
	if err != nil {
		return DriverObservation{}, ContextCatalog{}, err
	}
	index, found := worker.contextState.tabIndexes[tabID]
	if !found {
		return DriverObservation{}, ContextCatalog{}, ErrNotFound
	}
	if frameID != "" {
		if rawToken, ok := worker.contextState.frames[frameID]; !ok ||
			!rawCatalogHasFrame(raw, tabID, rawToken, worker.contextState.tabs) {
			return DriverObservation{}, ContextCatalog{}, ErrNotFound
		}
		if !catalogFrameReady(catalog, tabID, frameID) {
			return DriverObservation{}, ContextCatalog{}, ErrDenied
		}
	}
	if _, err = worker.callAndConsume(ctx, "browser_tabs", map[string]any{
		"action": "select", "index": index,
	}, true); err != nil {
		return DriverObservation{}, ContextCatalog{}, err
	}
	worker.contextState.selectedTabID = tabID
	worker.contextState.selectedFrame = frameID
	worker.contextState.generation++
	catalog, _, err = worker.probeContextsLocked(ctx)
	if err != nil {
		return DriverObservation{}, ContextCatalog{}, err
	}
	var observation DriverObservation
	if frameID == "" {
		observation, err = worker.observeLocked(ctx)
	} else {
		observation, err = worker.observeFrameLocked(ctx, tabID, frameID)
	}
	return observation, catalog, err
}

func (worker *playwrightWorker) CloseTab(ctx context.Context, tabID string) (ContextCatalog, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if err := worker.contextAvailableLocked(); err != nil {
		return ContextCatalog{}, err
	}
	catalog, _, err := worker.probeContextsLocked(ctx)
	if err != nil {
		return ContextCatalog{}, err
	}
	if len(catalog.Tabs) <= 1 {
		return ContextCatalog{}, ErrDenied
	}
	index, found := worker.contextState.tabIndexes[tabID]
	if !found {
		return ContextCatalog{}, ErrNotFound
	}
	if _, err = worker.callAndConsume(ctx, "browser_tabs", map[string]any{
		"action": "close", "index": index,
	}, true); err != nil {
		return ContextCatalog{}, err
	}
	delete(worker.contextState.tabs, tabID)
	delete(worker.contextState.tabIndexes, tabID)
	if worker.contextState.selectedTabID == tabID {
		worker.contextState.selectedTabID = ""
		worker.contextState.selectedFrame = ""
	}
	worker.contextState.generation++
	return worker.contextCatalogLocked(ctx)
}

func (worker *playwrightWorker) contextCatalogLocked(ctx context.Context) (ContextCatalog, error) {
	catalog, _, err := worker.probeContextsLocked(ctx)
	return catalog, err
}

func (worker *playwrightWorker) probeContextsLocked(
	ctx context.Context,
) (ContextCatalog, playwrightRawContextCatalog, error) {
	if err := worker.contextAvailableLocked(); err != nil {
		return ContextCatalog{}, playwrightRawContextCatalog{}, err
	}
	text, err := worker.callAndConsume(ctx, "browser_run_code_unsafe", map[string]any{
		"code": playwrightContextProbeCode,
	}, true)
	if err != nil {
		return ContextCatalog{}, playwrightRawContextCatalog{}, err
	}
	raw, err := parsePlaywrightContextProbe(text)
	if err != nil {
		worker.lost = true
		return ContextCatalog{}, playwrightRawContextCatalog{}, err
	}
	catalog, err := worker.projectContextCatalog(raw)
	if err != nil {
		worker.lost = true
		return ContextCatalog{}, playwrightRawContextCatalog{}, err
	}
	return catalog, raw, nil
}

func (worker *playwrightWorker) contextAvailableLocked() error {
	if worker.closing || worker.closed || worker.lost || worker.humanControl || worker.pendingDialog != nil {
		return ErrWorkerUnavailable
	}
	if !validIdentifier(worker.contextSessionID) || len(worker.contextSecret) != 32 {
		return ErrDriverIncompatible
	}
	return nil
}

func parsePlaywrightContextProbe(text string) (playwrightRawContextCatalog, error) {
	line, err := playwrightResultLine(text)
	if err != nil {
		return playwrightRawContextCatalog{}, err
	}
	prefix := playwrightContextMarker + "|ok|"
	if !strings.HasPrefix(line, prefix) {
		return playwrightRawContextCatalog{}, ErrDriverIncompatible
	}
	encoded := strings.TrimPrefix(line, prefix)
	decoded, err := url.QueryUnescape(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > playwrightContextResponseBytes {
		return playwrightRawContextCatalog{}, ErrDriverIncompatible
	}
	var catalog playwrightRawContextCatalog
	decoder := json.NewDecoder(strings.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&catalog) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		catalog.Generation == 0 || catalog.Selected == "" ||
		len(catalog.Pages) == 0 || len(catalog.Pages) > MaxContextTabs {
		return playwrightRawContextCatalog{}, ErrDriverIncompatible
	}
	seenPages := make(map[string]struct{}, len(catalog.Pages))
	seenFrames := make(map[string]struct{})
	selected := false
	for index, page := range catalog.Pages {
		if !playwrightPrivateContextToken.MatchString(page.Token) || page.Index != index ||
			page.Generation == 0 || len(page.Frames) > MaxContextFramesPerTab {
			return playwrightRawContextCatalog{}, ErrDriverIncompatible
		}
		if _, duplicate := seenPages[page.Token]; duplicate {
			return playwrightRawContextCatalog{}, ErrDriverIncompatible
		}
		seenPages[page.Token] = struct{}{}
		if page.Opener != "" {
			if _, exists := seenPages[page.Opener]; !exists {
				return playwrightRawContextCatalog{}, ErrDriverIncompatible
			}
		}
		selected = selected || page.Token == catalog.Selected
		pageFrames := make(map[string]struct{}, len(page.Frames))
		for _, frame := range page.Frames {
			if !playwrightPrivateContextToken.MatchString(frame.Token) || frame.Generation == 0 {
				return playwrightRawContextCatalog{}, ErrDriverIncompatible
			}
			if frame.Parent != "" {
				if _, exists := pageFrames[frame.Parent]; !exists {
					return playwrightRawContextCatalog{}, ErrDriverIncompatible
				}
			}
			if _, duplicate := seenFrames[frame.Token]; duplicate {
				return playwrightRawContextCatalog{}, ErrDriverIncompatible
			}
			seenFrames[frame.Token] = struct{}{}
			pageFrames[frame.Token] = struct{}{}
		}
	}
	if !selected {
		return playwrightRawContextCatalog{}, ErrDriverIncompatible
	}
	return catalog, nil
}

func playwrightResultLine(text string) (string, error) {
	const header = "### Result"
	if strings.Count(text, header) != 1 {
		return "", ErrDriverIncompatible
	}
	result := strings.TrimLeft(text[strings.Index(text, header)+len(header):], "\r\n")
	if end := strings.IndexByte(result, '\n'); end >= 0 {
		result = result[:end]
	}
	result = strings.Trim(result, "\r\"' ")
	if result == "" {
		return "", ErrDriverIncompatible
	}
	return result, nil
}

func (worker *playwrightWorker) projectContextCatalog(raw playwrightRawContextCatalog) (ContextCatalog, error) {
	state := &worker.contextState
	if state.catalogID == "" {
		state.catalogID = worker.opaqueContextID("catalog", "root")
		state.tabs = make(map[string]string)
		state.frames = make(map[string]string)
		state.tabSequence = make(map[string]uint64)
		state.frameSequence = make(map[string]uint64)
		state.tabIndexes = make(map[string]int)
	}
	if state.generation == 0 {
		state.generation = raw.Generation
	} else if raw.Generation > state.rawGeneration {
		state.generation += raw.Generation - state.rawGeneration
	}
	state.rawGeneration = raw.Generation
	reverseTabs := reverseContextIDs(state.tabs)
	reverseFrames := reverseContextIDs(state.frames)
	liveTabs := make(map[string]struct{}, len(raw.Pages))
	liveFrames := make(map[string]struct{})
	for _, page := range raw.Pages {
		if _, ok := reverseTabs[page.Token]; !ok {
			state.creation++
			id := worker.opaqueContextID("tab", page.Token)
			state.tabs[id], state.tabSequence[id], reverseTabs[page.Token] = page.Token, state.creation, id
		}
		for _, frame := range page.Frames {
			if _, ok := reverseFrames[frame.Token]; !ok {
				state.creation++
				id := worker.opaqueContextID("frame", frame.Token)
				state.frames[id], state.frameSequence[id], reverseFrames[frame.Token] = frame.Token, state.creation, id
			}
		}
	}
	tabs := make([]TabContext, 0, len(raw.Pages))
	for _, page := range raw.Pages {
		tabID := reverseTabs[page.Token]
		liveTabs[tabID] = struct{}{}
		state.tabIndexes[tabID] = page.Index
		tabURL, tabOrigin, locationErr := sanitizeContextLocation(page.URL)
		if locationErr != nil {
			return ContextCatalog{}, locationErr
		}
		tab := TabContext{
			ID: tabID, Kind: TabOpened, CreationSequence: state.tabSequence[tabID],
			DocumentGeneration: page.Generation, URL: tabURL, Origin: tabOrigin,
			Title: boundedContextLabel(page.Title),
		}
		if state.tabSequence[tabID] == 1 {
			tab.Kind = TabPrimary
		}
		for _, frame := range page.Frames {
			frameID := reverseFrames[frame.Token]
			liveFrames[frameID] = struct{}{}
			frameURL, frameOrigin, frameErr := sanitizeContextLocation(frame.URL)
			availability, safeFailure := FrameReady, ""
			if frameErr != nil {
				frameURL, frameOrigin, availability, safeFailure = initialBlankOrigin, initialBlankOrigin, FrameUnavailable, "frame_policy_denied"
			}
			depth := rawFrameDepth(page.Frames, frame.Token)
			if depth < 1 || depth > MaxContextFrameDepth {
				return ContextCatalog{}, ErrDriverIncompatible
			}
			tab.Frames = append(tab.Frames, FrameContext{
				ID:            frameID,
				ParentFrameID: reverseFrames[frame.Parent], CreationSequence: state.frameSequence[frameID],
				Depth: depth, DocumentGeneration: frame.Generation, URL: frameURL, Origin: frameOrigin,
				Label: boundedContextLabel(frame.Label), Availability: availability, SafeFailure: safeFailure,
			})
		}
		sort.SliceStable(
			tab.Frames,
			func(i, j int) bool { return tab.Frames[i].CreationSequence < tab.Frames[j].CreationSequence },
		)
		tabs = append(tabs, tab)
	}
	for id := range state.tabs {
		if _, live := liveTabs[id]; !live {
			delete(state.tabs, id)
			delete(state.tabIndexes, id)
		}
	}
	for id := range state.frames {
		if _, live := liveFrames[id]; !live {
			delete(state.frames, id)
		}
	}
	if state.selectedFrame != "" {
		if _, live := liveFrames[state.selectedFrame]; !live {
			state.selectedFrame = ""
			state.generation++
		}
	}
	sort.SliceStable(tabs, func(i, j int) bool { return tabs[i].CreationSequence < tabs[j].CreationSequence })
	selected := reverseTabs[raw.Selected]
	if state.selectedTabID == "" || state.selectedTabID != selected {
		state.selectedTabID, state.selectedFrame = selected, ""
	}
	catalog := ContextCatalog{
		ID: state.catalogID, Generation: state.generation,
		SelectedTabID: state.selectedTabID, SelectedFrameID: state.selectedFrame, Tabs: tabs,
	}
	if err := catalog.Validate(); err != nil {
		return ContextCatalog{}, errors.Join(ErrDriverIncompatible, err)
	}
	return catalog, nil
}

func reverseContextIDs(values map[string]string) map[string]string {
	reversed := make(map[string]string, len(values))
	for id, raw := range values {
		reversed[raw] = id
	}
	return reversed
}

func (worker *playwrightWorker) opaqueContextID(kind, raw string) string {
	digest := hmac.New(sha256.New, worker.contextSecret)
	_, _ = digest.Write(
		[]byte("mintclaw.browser.context.v1\x00" + kind + "\x00" + worker.contextSessionID + "\x00" + raw),
	)
	return kind + "_" + hex.EncodeToString(digest.Sum(nil)[:16])
}

func sanitizeContextLocation(raw string) (string, string, error) {
	if raw == initialBlankOrigin {
		return raw, raw, nil
	}
	return sanitizeObservedURL(raw)
}

func boundedContextLabel(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	for len(value) > MaxContextLabelBytes {
		value = value[:len(value)-1]
	}
	return value
}

func rawFrameDepth(frames []playwrightRawFrame, token string) int {
	parents := make(map[string]string, len(frames))
	for _, frame := range frames {
		parents[frame.Token] = frame.Parent
	}
	depth := 0
	for token != "" && depth <= MaxContextFrameDepth {
		depth++
		token = parents[token]
	}
	return depth
}

func rawCatalogHasFrame(raw playwrightRawContextCatalog, tabID, rawFrame string, tabs map[string]string) bool {
	rawTab := tabs[tabID]
	for _, page := range raw.Pages {
		if page.Token != rawTab {
			continue
		}
		for _, frame := range page.Frames {
			if frame.Token == rawFrame {
				return true
			}
		}
	}
	return false
}

func catalogFrameReady(catalog ContextCatalog, tabID, frameID string) bool {
	for _, tab := range catalog.Tabs {
		if tab.ID != tabID {
			continue
		}
		for _, frame := range tab.Frames {
			if frame.ID == frameID {
				return frame.Availability == FrameReady
			}
		}
	}
	return false
}

func (worker *playwrightWorker) observeLocked(ctx context.Context) (DriverObservation, error) {
	if worker.pendingDialog != nil {
		return worker.pendingDialogObservationLocked()
	}
	text, err := worker.callAndConsume(ctx, "browser_snapshot", map[string]any{"boxes": false}, true)
	if err != nil {
		return DriverObservation{}, err
	}
	observation, err := parsePlaywrightObservation(
		text,
		worker.limits.SnapshotBytes,
		worker.limits.SnapshotRefs,
		worker.limits.ToolResultBytes,
	)
	if err == nil {
		worker.lastObservation = observation
	}
	return observation, err
}

func (worker *playwrightWorker) observeFrameLocked(
	ctx context.Context,
	tabID, frameID string,
) (DriverObservation, error) {
	rawTab, rawFrame := worker.contextState.tabs[tabID], worker.contextState.frames[frameID]
	code := fmt.Sprintf(`async (page) => {
  const state = page.context()[Symbol.for("mintclaw.browser.context-registry.v1")];
  if (!state) return "MINTCLAW_CONTEXT_V1|error|missing_registry";
  const targetPage = page.context().pages().find(candidate => state.pages.get(candidate)?.token === %s);
  const frame = targetPage?.frames().find(candidate => state.frames.get(candidate)?.token === %s);
  if (!frame) return "MINTCLAW_CONTEXT_V1|error|frame_detached";
  const snapshot = await frame.locator("body").ariaSnapshot();
  return "MINTCLAW_CONTEXT_V1|frame|" + encodeURIComponent(JSON.stringify({
    url: frame.url(), title: await targetPage.title(), snapshot
  }));
}`, strconv.Quote(rawTab), strconv.Quote(rawFrame))
	text, err := worker.callAndConsume(ctx, "browser_run_code_unsafe", map[string]any{"code": code}, true)
	if err != nil {
		return DriverObservation{}, err
	}
	line, err := playwrightResultLine(text)
	if err != nil || !strings.HasPrefix(line, playwrightContextMarker+"|frame|") {
		return DriverObservation{}, ErrDriverIncompatible
	}
	decoded, err := url.QueryUnescape(strings.TrimPrefix(line, playwrightContextMarker+"|frame|"))
	if err != nil || len(decoded) > worker.limits.ToolResultBytes {
		return DriverObservation{}, ErrDriverIncompatible
	}
	var result struct{ URL, Title, Snapshot string }
	if json.Unmarshal([]byte(decoded), &result) != nil {
		return DriverObservation{}, ErrDriverIncompatible
	}
	safeURL, origin, err := sanitizeContextLocation(result.URL)
	if err != nil {
		return DriverObservation{}, ErrDenied
	}
	truncated := false
	if len(result.Snapshot) > worker.limits.SnapshotBytes {
		result.Snapshot = truncateUTF8(result.Snapshot, worker.limits.SnapshotBytes)
		truncated = true
	}
	return DriverObservation{
		URL:       safeURL,
		Origin:    origin,
		Title:     boundedContextLabel(result.Title),
		Snapshot:  result.Snapshot,
		Truncated: truncated,
	}, nil
}

func truncateUTF8(value string, maximum int) string {
	if maximum < 1 {
		return ""
	}
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for len(value) != 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
