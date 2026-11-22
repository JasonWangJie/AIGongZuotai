import assert from 'node:assert/strict';
import { setTimeout as sleep } from 'node:timers/promises';
import { until, withLocalBrowser } from './local-browser.mjs';

// Keep numeric sequence IDs up to the declared fixture size. Each event body
// is inspected once and discarded before Chromium dispatches the next event.
function installObserver(expectedSegments) {
  const state = {
    creates: 0,
    turns: 0,
    conversationId: '',
    runId: '',
    wrongRun: false,
    runStarts: 0,
    runEnds: 0,
    runOpens: 0,
    deltas: 0,
    completions: 0,
    timersStarted: 0,
    timersFired: 0,
    timersCleared: 0,
    peakTimers: 0,
    streamFirst: 0,
    streamLast: 0,
    longTasks: 0,
    longestTaskMs: 0,
    sequence: new Uint16Array(expectedSegments + 1),
    eventCount: 0,
    validStream: true,
    maxGapMs: 0,
    projectStarts: 0,
    projectEnds: 0,
    latestProject: null,
    activeTimers: new Set(),
    timerTrackingOverflow: false,
    wheel: null,
    input: null,
    isStreaming: false,
  };
  window.longReplyObservation = state;
  const fetch = window.fetch;
  window.fetch = function (resource, options) {
    const url = new URL(resource instanceof Request ? resource.url : resource, location.href);
    const method = options?.method || (resource instanceof Request ? resource.method : 'GET');
    if (url.origin === location.origin && method === 'POST') {
      if (url.pathname === '/api/v1/conversations') state.creates++;
      const match = /^\/api\/v1\/conversations\/([^/]+)\/runs$/.exec(url.pathname);
      if (match) {
        state.conversationId = match[1];
        state.turns++;
      }
    }
    return Reflect.apply(fetch, this, arguments);
  };
  const Native = window.EventSource;
  window.EventSource = new Proxy(Native, {
    construct(target, args) {
      const source = Reflect.construct(target, args);
      const url = new URL(args[0], location.href);
      const run = /^\/api\/v1\/runs\/([^/]+)\/events$/.exec(url.pathname);
      const project = /^\/api\/v1\/workspaces\/[^/]+\/events$/.test(url.pathname);
      if (url.origin !== location.origin || (!run && !project)) return source;
      if (run) {
        state.runStarts++;
        if (state.runId && state.runId !== run[1]) state.wrongRun = true;
        state.runId = run[1];
        source.addEventListener('open', () => state.runOpens++);
        function recordEvent(event, terminal) {
          const index = state.eventCount++;
          const seq = Number(event.lastEventId);
          if (
            index >= state.sequence.length ||
            !Number.isSafeInteger(seq) ||
            event.lastEventId !== String(seq) ||
            seq !== index + 1
          ) {
            state.validStream = false;
            return;
          }
          state.sequence[index] = seq;
          try {
            const data = JSON.parse(event.data);
            const n = String(index).padStart(4, '0');
            const matches = terminal
              ? index === expectedSegments && data.status === 'COMPLETED'
              : index < expectedSegments && data.delta === '段' + n + '\n.' + n + '\n';
            if (data.run_id !== state.runId || data.seq !== seq || !matches) state.validStream = false;
          } catch {
            state.validStream = false;
          }
        }
        source.addEventListener('message.delta', (event) => {
          recordEvent(event, false);
          const now = performance.now();
          if (!state.deltas) state.streamFirst = now;
          else state.maxGapMs = Math.max(state.maxGapMs, now - state.streamLast);
          state.streamLast = now;
          state.isStreaming = true;
          state.deltas++;
        });
        source.addEventListener('run.completed', (event) => {
          recordEvent(event, true);
          state.completions++;
          state.isStreaming = false;
        });
      } else {
        state.projectStarts++;
        state.latestProject = new WeakRef(source);
      }
      const close = source.close.bind(source);
      let closed = false;
      source.close = () => {
        if (!closed) {
          if (run) state.runEnds++;
          else state.projectEnds++;
          closed = true;
        }
        return close();
      };
      return source;
    },
  });
  // useStudio schedules the 40ms public text flush. Other app timers keep
  // native behavior, and are outside the evidence reported for this timer.
  const setTimer = window.setTimeout.bind(window);
  const clearTimer = window.clearTimeout.bind(window);
  window.setTimeout = function (callback, delay, ...args) {
    if (delay !== 40 || typeof callback !== 'function') return setTimer(callback, delay, ...args);
    const id = setTimer(
      function (...params) {
        state.activeTimers.delete(id);
        state.timersFired++;
        return Reflect.apply(callback, this, params);
      },
      delay,
      ...args,
    );
    if (state.activeTimers.size < 64) state.activeTimers.add(id);
    else state.timerTrackingOverflow = true;
    state.timersStarted++;
    state.peakTimers = Math.max(state.peakTimers, state.activeTimers.size);
    return id;
  };
  window.clearTimeout = function (id) {
    if (state.activeTimers.delete(id)) state.timersCleared++;
    clearTimer(id);
  };
  const longTasks = new PerformanceObserver((entries) => {
    if (!state.isStreaming) return;
    for (const e of entries.getEntries()) {
      state.longTasks++;
      state.longestTaskMs = Math.max(state.longestTaskMs, e.duration);
    }
  });
  longTasks.observe({ type: 'longtask' });
  document.addEventListener(
    'input',
    (event) => {
      if (!event.isTrusted || !state.input || event.target.id !== 'chat-input') return;
      const probe = state.input;
      requestAnimationFrame(() =>
        requestAnimationFrame(() => {
          const count = document.querySelector('.chat-composer-footer > span');
          if (
            event.target.value === probe.expected &&
            count?.textContent.split(' / ')[0] === String(probe.expected.length)
          ) {
            probe.paintedMs = performance.now() - event.timeStamp;
          }
        }),
      );
    },
    true,
  );
  document.addEventListener(
    'wheel',
    (event) => {
      const probe = state.wheel;
      if (!probe || !event.isTrusted || !event.target.closest('.chat-timeline')) return;
      const timeline = document.querySelector('.chat-timeline');
      let frames = 0;
      const check = () => {
        if (state.wheel !== probe) return;
        const displacement = timeline.scrollTop - probe.before;
        if (displacement * probe.direction > 0) {
          probe.distance = Math.abs(displacement);
          probe.paintedMs = performance.now() - event.timeStamp;
        } else if (++frames < 30) {
          requestAnimationFrame(check);
        }
      };
      requestAnimationFrame(check);
    },
    { capture: true, passive: true },
  );
}

const content = () => document.querySelector('.chat-message.assistant .chat-message-content')?.textContent;

// Verify the rendered DOM at interaction time. Only complete, exact segments
// count; a later snapshot cannot replace a missed or stale early sample.
async function readPrefix(page, progress, expectedText, segmentEnds, previous, ordinal) {
  const view = await page.evaluate(() => {
    const state = window.longReplyObservation;
    return {
      active: Boolean(document.querySelector('.chat-message-status.active')),
      text: document.querySelector('.chat-message.assistant .chat-message-content')?.textContent,
      received: state.deltas,
      valid: state.validStream,
      elapsed: performance.now() - state.streamFirst,
    };
  });
  return verifyPrefix(view, progress().sent, expectedText, segmentEnds, previous, ordinal);
}

export function verifyPrefix(view, sent, expectedText, segmentEnds, previous, ordinal) {
  assert.ok(view.active && view.valid, 'inactive or invalid stream at interaction');
  assert.ok(
    view.text && view.text.length < expectedText.length && expectedText.startsWith(view.text),
    'visible prefix mismatch',
  );
  const rendered = segmentEnds.get(view.text.length);
  const bytes = Buffer.byteLength(view.text);
  assert.ok(
    rendered > (previous?.rendered_segments ?? 0) && bytes > (previous?.visible_utf8_bytes ?? 0),
    'visible prefix failed to advance',
  );
  assert.ok(
    rendered <= view.received && view.received <= sent && sent - rendered <= 2,
    'visible stream lag exceeded two segments',
  );
  return {
    ordinal,
    elapsed_ms: Math.round(view.elapsed),
    fixture_sent_segments: sent,
    browser_received_segments: view.received,
    rendered_segments: rendered,
    visible_utf8_bytes: bytes,
    lag_segments: sent - rendered,
  };
}

async function sample(page) {
  await page.command('HeapProfiler.collectGarbage');
  const heap = await page.command('Runtime.getHeapUsage');
  const dom = await page.command('Memory.getDOMCounters');
  const observed = await page.evaluate(() => ({
    assistant_nodes: document.querySelectorAll('.chat-message.assistant').length,
    user_nodes: document.querySelectorAll('.chat-message.user').length,
    indexed_messages: Number(document.querySelector('.chat-window-list')?.dataset.indexedCount || 0),
    resident_messages: Number(document.querySelector('.chat-window-list')?.dataset.residentCount || 0),
    mounted_rows: document.querySelectorAll('.chat-window-row').length,
    attached_media: document.querySelectorAll('audio, video').length,
    active_run_subscriptions: window.longReplyObservation.runStarts - window.longReplyObservation.runEnds,
    active_flush_timers: window.longReplyObservation.activeTimers.size,
  }));
  return {
    ...observed,
    js_heap_bytes_after_gc: heap.usedSize,
    dom_nodes_after_gc: dom.nodes,
    documents_after_gc: dom.documents,
    event_listeners_after_gc: dom.jsEventListeners,
  };
}

async function waitForSent(progress, target, expires, signal) {
  while (progress().sent < target) {
    signal?.throwIfAborted();
    if (Date.now() > expires) throw Error('Fixture progress timeout');
    await sleep(100, undefined, { signal });
  }
}

const summarize = (values) => {
  assert.ok(values.length > 0 && values.length <= 32);
  const sorted = [...values].sort((a, b) => a - b);
  return {
    n: sorted.length,
    min: sorted[0],
    p50: sorted[Math.ceil(sorted.length * 0.5) - 1],
    p95: sorted[Math.ceil(sorted.length * 0.95) - 1],
    max: sorted.at(-1),
  };
};

async function typeDuringReply(page, draft) {
  const next = draft + 'a';
  await page.evaluate((expected) => {
    const input = document.querySelector('#chat-input');
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
    window.longReplyObservation.input = { expected };
  }, next);
  await page.command('Input.insertText', { text: 'a' });
  const latency = await until(
    () => page.evaluate(() => window.longReplyObservation.input?.paintedMs),
    'trusted draft input',
    3000,
  );
  return { draft: next, latency };
}

async function scrollDuringReply(page) {
  const point = await until(
    () =>
      page.evaluate(() => {
        const timeline = document.querySelector('.chat-timeline');
        if (!timeline || timeline.scrollHeight - timeline.clientHeight < 72) return false;
        timeline.scrollIntoView({ block: 'center', behavior: 'instant' });
        const r = timeline.getBoundingClientRect();
        const x = r.left + r.width / 2;
        const y = Math.max(1, r.top) + (Math.min(innerHeight, r.bottom) - Math.max(1, r.top)) / 2;
        if (!timeline.contains(document.elementFromPoint(x, y))) return false;
        const direction = timeline.scrollTop >= 32 ? -1 : 1;
        window.longReplyObservation.wheel = { direction, before: timeline.scrollTop };
        return { x, y, deltaY: direction * 160 };
      }),
    'scrollable live transcript',
    4000,
  );
  await page.command('Input.dispatchMouseEvent', { type: 'mouseWheel', deltaX: 0, ...point });
  return until(
    () =>
      page.evaluate(() => {
        const p = window.longReplyObservation.wheel;
        return p?.paintedMs && { latency: p.paintedMs, distance: p.distance };
      }),
    'trusted wheel displacement',
    3000,
  );
}

async function switchProject(page, original) {
  await page.evaluate(() => document.querySelector('button[aria-label="创建项目"]').click());
  await page.fill('#project-name', '持续回复切换');
  await page.trustedButton('创建并进入');
  await until(
    () =>
      page.evaluate((oldProject) => {
        const select = document.querySelector('#project-select');
        const s = window.longReplyObservation;
        const current = s.latestProject?.deref();
        return (
          select?.value &&
          select.value !== oldProject &&
          s.projectStarts - s.projectEnds === 1 &&
          current?.readyState === EventSource.OPEN &&
          new URL(current.url).searchParams.get('project_id') === select.value &&
          document.querySelector('.chat-no-selection') &&
          !document.querySelector('.chat-message')
        );
      }, original),
    'new project and previous resources released',
  );
}

export async function observeLongReply({
  origin,
  signal,
  expectedText,
  segments,
  durationMs,
  progress,
  browserReady,
  chromePath,
  identity,
  projectId,
  onBrowserStart,
}) {
  assert.ok(Buffer.byteLength(expectedText) <= 128 * 1024 && expectedText.length > 0);
  assert.ok(Number.isSafeInteger(durationMs) && durationMs >= 10000 && durationMs <= 2100000);
  assert.ok(
    Array.isArray(segments) &&
      segments.length >= 2 &&
      segments.length <= 1801 &&
      segments.length === progress().total &&
      segments.join('') === expectedText,
  );
  const segmentEnds = new Map();
  let endOffset = 0;
  for (const [i, segment] of segments.entries()) {
    assert.ok(segment.length > 0);
    endOffset += segment.length;
    segmentEnds.set(endOffset, i + 1);
  }
  const inputTimes = [],
    wheelTimes = [],
    distances = [];
  const measurements = {
    viewport_css_px: [1280, 800],
    max_allowed_lag_segments: 2,
    prefix_samples: [],
    samples: {},
  };
  const expires = Date.now() + durationMs + 45000;
  const launchEnv = {};
  for (const key of [
    'SystemRoot',
    'WINDIR',
    'PATH',
    'TEMP',
    'TMP',
    'TMPDIR',
    'HOME',
    'USERPROFILE',
    'LOCALAPPDATA',
    'APPDATA',
    'LANG',
    'LC_ALL',
    'XDG_RUNTIME_DIR',
  ]) {
    if (process.env[key]) launchEnv[key] = process.env[key];
  }
  let result;
  await withLocalBrowser(
    { url: origin, onlyOrigin: true, signal, chromePath, launchEnv, onChildStart: onBrowserStart },
    async (open) => {
      const page = await open({ width: 1280, height: 800, deferNavigation: true });
      await page.command('Page.addScriptToEvaluateOnNewDocument', {
        source: '(' + installObserver.toString() + ')(' + segments.length + ')',
      });
      await page.command('Page.navigate', { url: origin });
      measurements.chromium_product = (await page.command('Browser.getVersion')).product;
      if (identity) {
        await page.fill('#auth-email', identity.email);
        await page.fill('#auth-password', identity.password);
        await page.trustedButton('进入创作空间');
      }
      const project = await until(
        () => page.evaluate(() => document.querySelector('#project-select:not(:disabled)')?.value),
        'original project',
      );
      if (projectId || identity?.projectId) {
        assert.ok(project === (projectId || identity.projectId), 'unexpected original project');
      }
      await page.button('新的灵感对话');
      await page.fill('#conversation-title', '持续回复测量');
      await page.trustedButton('新建会话');
      await until(
        () => page.evaluate(() => Boolean(document.querySelector('#chat-input:not(:disabled)'))),
        'new conversation',
      );
      measurements.samples.before_send = await sample(page);
      await page.fill('#chat-input', '本机持续回复验收');
      await page.trustedButton('发送第一条');
      await until(
        () => page.evaluate(() => window.longReplyObservation.runOpens === 1),
        'browser Run stream open',
      );
      browserReady();
      await until(
        () =>
          page.evaluate(
            () =>
              window.longReplyObservation.deltas > 0 &&
              Boolean(document.querySelector('.chat-message-status.active')),
          ),
        'live run subscription',
      );

      // One trusted input and scroll per minute in the full run. The short mode
      // measures the same controls three times across its actual stream.
      const count = Math.max(3, Math.min(30, Math.floor(durationMs / 60000)));
      let draft = '';
      for (let i = 0; i < count; i++) {
        const fraction = count === 3 ? [0.32, 0.56, 0.8][i] : (i + 1) / (count + 1);
        await waitForSent(progress, Math.ceil(progress().total * fraction), expires, signal);
        const probe = await readPrefix(
          page,
          progress,
          expectedText,
          segmentEnds,
          measurements.prefix_samples.at(-1),
          i + 1,
        );
        measurements.prefix_samples.push(probe);
        const typed = await typeDuringReply(page, draft);
        draft = typed.draft;
        inputTimes.push(typed.latency);
        const wheel = await scrollDuringReply(page);
        wheelTimes.push(wheel.latency);
        distances.push(wheel.distance);
        assert.ok(
          await page.evaluate(() => Boolean(document.querySelector('.chat-message-status.active'))),
          'interaction missed live reply',
        );
        if (i === 0 || i === Math.floor(count / 2) || i === count - 1) {
          const s = await sample(page);
          assert.equal(s.indexed_messages, 2, 'both original messages remain indexed');
          assert.equal(s.resident_messages, 2, 'both original message bodies remain resident');
          assert.equal(s.assistant_nodes, 1, 'one active assistant row');
          assert.ok(s.user_nodes === 0 || s.user_nodes === 1, 'at most one mounted user row');
          assert.equal(s.mounted_rows, s.assistant_nodes + s.user_nodes, 'every mounted row contains one message');
          assert.equal(s.active_run_subscriptions, 1);
          assert.equal(s.attached_media, 0);
          measurements.samples[i === 0 ? 'early' : i === count - 1 ? 'late' : 'middle'] = {
            ...s,
            visible_utf8_bytes: probe.visible_utf8_bytes,
          };
        }
      }
      await waitForSent(progress, progress().total, expires, signal);
      await until(
        () =>
          page.evaluate(
            () =>
              document.querySelector('.chat-message-status')?.textContent === '已完成' &&
              !document.querySelector('.stream-cursor') &&
              window.longReplyObservation.completions === 1,
          ),
        'rendered terminal result',
      );
      assert.ok((await page.evaluate(content)) === expectedText, 'final text mismatch');
      assert.ok(
        await page.evaluate((expected) => document.querySelector('#chat-input')?.value === expected, draft),
        'unsent draft lost',
      );
      measurements.samples.completed = await sample(page);
      // A long assistant row can push the user's message outside the render
      // window. Verify the original prompt through the actual timeline.
      await page.evaluate(() => document.querySelector('.chat-timeline').scrollTo({ top: 0, behavior: 'instant' }));
      await until(() => page.evaluate(() =>
        document.querySelector('.chat-window-row[data-index="0"] .chat-message.user .chat-message-content')?.textContent === '本机持续回复验收'
      ), 'original user row after scrolling back');
      await page.evaluate(() => {
        const timeline = document.querySelector('.chat-timeline');
        timeline.scrollTo({ top: timeline.scrollHeight, behavior: 'instant' });
      });
      await until(async () => (await page.evaluate(content)) === expectedText, 'original completed assistant row after returning to bottom');
      measurements.original_prompt_read_back = true;
      await switchProject(page, project);
      measurements.samples.after_project_switch = await sample(page);
      const end = await page.evaluate(() => {
        const s = window.longReplyObservation;
        return {
          runId: s.runId,
          conversationId: s.conversationId,
          wrongRun: s.wrongRun,
          creates: s.creates,
          turns: s.turns,
          run_starts: s.runStarts,
          run_opens: s.runOpens,
          run_closes: s.runEnds,
          delta_events: s.deltas,
          completed_events: s.completions,
          received_sequence: Array.from(s.sequence),
          valid_stream: s.validStream,
          max_delta_gap_ms: s.maxGapMs,
          first_to_last_delta_ms: s.streamLast - s.streamFirst,
          project_subscriptions: s.projectStarts - s.projectEnds,
          project_subscriptions_started: s.projectStarts,
          project_subscriptions_closed: s.projectEnds,
          timer_tracking_overflow: s.timerTrackingOverflow,
          flush_timers_started: s.timersStarted,
          flush_timers_fired: s.timersFired,
          flush_timers_cleared: s.timersCleared,
          flush_timers_peak: s.peakTimers,
          long_tasks_while_streaming: s.longTasks,
          longest_observed_task_ms: s.longestTaskMs,
        };
      });
      const { runId, conversationId, wrongRun, ...counters } = end;
      assert.ok(runId && conversationId && !wrongRun);
      assert.equal(end.creates, 1);
      assert.equal(end.turns, 1);
      assert.equal(end.run_starts, 1);
      assert.equal(end.run_opens, 1);
      assert.equal(end.run_closes, 1);
      assert.ok(
        end.valid_stream &&
          end.delta_events === segments.length &&
          end.completed_events === 1 &&
          end.flush_timers_started > 0,
      );
      assert.ok(
        end.received_sequence.every((seq, index) => seq === index + 1),
        'incomplete browser sequence',
      );
      assert.ok(
        end.first_to_last_delta_ms >= durationMs &&
          end.max_delta_gap_ms <= (5 * durationMs) / (segments.length - 1),
        'browser stream duration or cadence differed',
      );
      assert.equal(end.flush_timers_peak, 1);
      assert.equal(end.timer_tracking_overflow, false);
      assert.ok(end.project_subscriptions_started >= 2 && end.project_subscriptions_closed >= 1);
      assert.equal(end.flush_timers_started, end.flush_timers_fired + end.flush_timers_cleared);
      for (const s of [measurements.samples.completed, measurements.samples.after_project_switch]) {
        assert.equal(s.active_flush_timers + s.active_run_subscriptions + s.attached_media, 0);
      }
      assert.equal(
        measurements.samples.after_project_switch.assistant_nodes +
          measurements.samples.after_project_switch.user_nodes,
        0,
      );
      Object.assign(measurements, {
        observed: counters,
        visible_output_utf8_bytes: Buffer.byteLength(expectedText),
        final_text_exact: true,
        unsent_draft_preserved: true,
        input_to_two_frames_ms: summarize(inputTimes),
        wheel_to_observed_scroll_ms: summarize(wheelTimes),
        wheel_displacement_css_px: summarize(distances),
      });
      await page.close();
      result = { runId, conversationId, measurements };
    },
  );
  return result;
}
