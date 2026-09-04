---
title: "Hosted STT APIs & Post-Processing Layer (Archived / Reference)"
author: "Matthew Schulkind"
date: 2026-08-15
status: deprecated
tags: [research, cloud, mavor, apis, post-processing]
summary: "Comparative analysis of hosted transcription APIs (Groq, OpenAI, Deepgram) and LLM post-processing layers. Kept as architectural reference."
---

# Hosted STT APIs and the Post-Processing Layer

Evergreen domain doc for `mavor`, the Sway/Wayland voice-dictation daemon. Scope:
**hosted (cloud) speech-to-text (STT) backends** and the **LLM post-processing layer**
that turns a raw transcript into text you'd actually want typed into your editor.

> [!NOTE]
> The `mavor` project scope has been narrowed to 100% local, generic GPU/CPU execution. This document is preserved as an architectural reference.

Sibling docs in this tree cover open-weight acoustic models, local inference
runtimes, and Wayland plumbing. This doc deliberately does not re-derive those;
where the boundary is fuzzy (e.g. a Whisper LoRA that does cleanup during
decoding) it is flagged and cross-referenced rather than duplicated.

**Project context.** [`../../internal/speech/speech.go`](../../internal/speech/speech.go) defines a one-method
`Transcriber` interface with a single real implementation (`WhisperCli`, shelling
out to `whisper-cli`) and a `Mock`. [`../../internal/daemon/daemon.go`](../../internal/daemon/daemon.go) calls
`d.transcriber.Transcribe(ctx, wav)` from one place, then hands the string to
`d.output.Emit`. That is the entire seam a hosted backend has to fit through, and
it is already the right shape. Config
([`../../internal/config/config.go`](../../internal/config/config.go)) is flat TOML with a whisper-specific
`model`/`model_dir` pair — that part is not yet the right shape (see §7).

---

## 0. Verdicts at a glance

| Option | Verdict for this project |
| --- | --- |
| **Groq** (`whisper-large-v3-turbo`) | **Adopt as the reference hosted backend.** Cheapest by an order of magnitude, fastest by a wide margin, OpenAI-compatible endpoint, API key is the whole setup. Its 10s minimum billing is irrelevant when a clip costs ~0.0001¢. |
| **OpenAI** (`gpt-transcribe` / `gpt-4o-mini-transcribe`) | **Shortlisted as the second backend.** Per-second billing with no minimum, a `prompt` field for vocabulary steering, and the same wire format as Groq — so one HTTP client covers both. Priced ~7–11× Groq. |
| **Deepgram Nova line** | **Shortlisted, conditional.** See §2; strong keyterm prompting is the single best answer to "it keeps writing *cubernetties*". Worth it only if custom vocabulary turns out to be the binding accuracy constraint. |
| **AssemblyAI** | **Not for v1.** Built around long-form media + audio-intelligence add-ons; short-utterance dictation is not its shape. |
| **ElevenLabs Scribe** | **Not for v1.** Accuracy-competitive, but priced and positioned for media transcription. |
| **Speechmatics** | **Rejected for v1.** Enterprise sales motion; setup cost is not "an API key". |
| **Azure / Google / AWS** | **Rejected for v1.** Each costs a cloud account, a project/resource, and a non-trivial auth story (SigV4, service-account JSON, resource keys) before the first word is transcribed. That fails the "API key is the only setup cost" bar for a single-user desktop tool. |
| **Mistral Voxtral API** | **Watch.** Interesting because it is transcription *and* understanding in one model — but that is the post-processing layer, and see §5 for why fusing them is a mixed blessing. |
| **Fireworks / Together** | **Watch.** Commodity Whisper hosting; no advantage over Groq unless you already have an account. |
| **LLM post-processing, local small model** | **Adopt as the default post-processor** (opt-in, off by default). Keeps the privacy story intact. Costs ~0.3–1.5s on a GPU, which is real but tolerable. |
| **LLM post-processing, hosted (Claude Haiku 4.5)** | **Shortlisted as an alternative post-processor.** ~0.13¢ per dictation and ~1–1.5s. Only sane if the transcript already left the machine. |
| **Streaming / realtime STT** | **Rejected for v1, revisit for v2.** Real win (§6) but it inverts the daemon's architecture: `Recorder.Stop() → wav → Transcribe(path)` becomes a duplex stream. Not worth it until the batch path is solid. |

---

## 1. The latency budget

Everything in this doc is a trade against one number: **the wall time between
releasing the hotkey and text appearing in the focused window.** Get this wrong
and the tool is worse than typing, regardless of accuracy.

Reference points from shipping dictation products (checked 2026-08-15, vendor and
review figures, not independently measured):

| Product | Reported end-to-end latency |
| --- | --- |
| Aqua Voice | 450 ms (vendor); 450 ms–1 s in review |
| Wispr Flow | ~700 ms (review); one measurement reports 1,805 ms average |
| "Most cloud-based tools" | 500–800 ms |

Treat **~500 ms as good, ~1 s as acceptable, >2 s as the point where users stop
reaching for the hotkey.** Note that the Wispr numbers disagree by 2.5×
across reviewers, which tells you these figures are soft — measure your own.

The budget for this project, batch (non-streaming) path:

```
release hotkey
  ├─ parec teardown + WAV finalize        ~10–50 ms   (local, existing)
  ├─ upload N seconds of audio            ~50–300 ms  (network, size-dependent)
  ├─ hosted transcription                 ~200–800 ms (see §2)
  ├─ [optional] LLM post-processing       ~300–1500 ms (see §5)  ← the expensive one
  └─ wtype + wl-copy                      ~10–50 ms   (local, existing)
```

Two consequences that shape every recommendation below:

1. **The post-processing pass can easily cost more than the transcription.** A
   1.5 s Haiku call bolted onto a 300 ms Groq transcription triples the wait. Any
   design that makes post-processing mandatory is a design that makes the tool
   feel slow.
2. **Audio upload time scales with clip length; transcription time on a fast
   provider barely does.** Groq transcribes at 217–228× real-time
   ([source](https://www.cloudzero.com/blog/groq-pricing/), checked 2026-08-15) —
   a 20-second clip is ~90 ms of actual compute. For short-utterance dictation
   the hosted path is dominated by network round-trip and TLS, not inference.
   This is the core reason hosted STT is attractive here: it converts a
   compute-bound local step into a latency-bound network step, and for 10-second
   clips the network wins.

**Corollary worth internalizing:** because compute is ~free at this clip length,
provider choice for *latency* is mostly a question of geography and connection
setup, not model speed. Keep the HTTP client's connections warm (`http.Client`
with a shared `Transport`, HTTP/2, and a keep-alive ping while the overlay is
showing "Recording") — that is likely worth more milliseconds than switching
providers.

**Encode before uploading.** This is arithmetic rather than a citation, but it
falls straight out of the current pipeline. `parec` produces 16 kHz mono
16-bit PCM = **32 kB/s**, so a 15-second dictation is a **~480 kB** WAV. On a
typical residential uplink (10 Mbit/s) that is **~400 ms of upload — larger than
the entire transcription step on a fast provider.** Re-encoding to Opus at
~24 kbps brings the same clip to ~45 kB, a ~10× reduction and well under
50 ms. Every major transcription API accepts compressed audio (Opus/OGG/FLAC/MP3
are universally supported; confirm the exact list per provider), so this is close
to free latency. **Uploading raw WAV would be the single largest avoidable cost
in the hosted path**, and it is the sort of thing that is easy to get wrong by
simply passing through the file the local backend already wanted.

---

## 2. Hosted transcription APIs

> Prices, model names, and latency figures in this section are **perishable** —
> the consolidated re-check list is in [§8](#8-fast-moving--verify-before-building).

### 2.1 The billing-increment trap

The headline "per audio hour" price is misleading for this workload. A dictation
utterance is 5–20 seconds. A provider that bills a 1-minute minimum charges
**3–12× its headline rate** for a typical clip.

| Provider | Billing increment | Effective cost of a 10 s clip |
| --- | --- | --- |
| OpenAI | Rounds to nearest second, **no minimum** ([costgoat, Aug 2026](https://costgoat.com/pricing/openai-transcription)) | true pro-rata |
| Groq | **10-second minimum** per request ([source](https://www.cloudzero.com/blog/groq-pricing/), checked 2026-08-15) | ~1× (a dictation clip is already ≥10 s, mostly) |
| Others | *see per-provider notes* | |

Check this explicitly before adopting anyone. It is the single most commonly
mis-modelled cost in short-utterance workloads.

That said — put the numbers in perspective before optimizing them. At Groq's
$0.04/audio-hour, **100 dictations a day of 15 seconds each costs about 5 cents a
month.** At OpenAI's $0.0045/min `gpt-transcribe`, the same usage is about $1.70
a year. *Transcription cost is not a real decision variable for a single-user
desktop tool.* Latency, accuracy, and privacy are. Do not let a pricing table
drive this decision; it is included below for completeness and because the same
analysis changes if this ever becomes a multi-user product.

### 2.2 Comparison table

All figures **checked 2026-08-15**. Reference workload: 12-second clips, 100/day
= 10 audio-hours/month, single user, English, one-shot batch.

| Provider / model | Mode | $/hour | Per 12 s clip | 10 hr/mo | WER | Speed (audio s/s) |
| :--- | :--- | ---: | ---: | ---: | ---: | ---: |
| Speechmatics Melia 1 | batch | 0.129 | $0.00043 | $1.29 | 4.9% | 204× |
| AssemblyAI Universal-3.5 Pro | async | 0.21 | $0.00070 | $2.10 | 3.0% | 110× |
| ElevenLabs Scribe v2 | batch | 0.22 | $0.00073 | $2.20 | **2.2%** | 57× |
| Speechmatics Standard | batch | 0.24 | $0.00080 | $2.40 | — | — |
| Deepgram Nova-3 mono | batch | 0.258 | $0.00086 | $2.58 | 5.2% | **427×** |
| Deepgram Nova-3 multilingual | batch | 0.312 | $0.00104 | $3.12 | — | — |
| Speechmatics Enhanced | batch | 0.40 | $0.00133 | $4.00 | 4.0% | 69× |
| AssemblyAI Sync API | **sync** | 0.45 | $0.00150 | $4.50 | 3.0% | — |

Streaming rates, for completeness: AssemblyAI Universal-Streaming $0.15/hr ·
Deepgram Nova-3 $0.288 promo (list $0.462) · ElevenLabs Scribe v2 Realtime $0.39 ·
Deepgram Flux English $0.39 promo · Speechmatics RT Enhanced $0.43 ·
AssemblyAI U3.5 Pro Realtime $0.45.

WER and speed are Artificial Analysis AA-WER v2 (~8 h: AA-AgentTalk 50%,
VoxPopuli 25%, Earnings22 25%); speed is measured on 10-minute audio, so the
"per 12 s clip" compute implied by it (Deepgram ~28 ms, AssemblyAI ~110 ms,
ElevenLabs ~210 ms) is a floor, not a round trip.

> [!IMPORTANT]
> **The cost column is noise at this workload.** Every option lands between
> $1.29 and $4.50 a month, and free credits swamp all of it: Deepgram $200 with
> no card (~775 hours ≈ 6.5 years at this volume), Speechmatics $100 no card,
> AssemblyAI $50 no card. Decide on latency, accuracy, and privacy.

**API shape decides more than price does.** Three of these return the transcript
in the HTTP response; one makes you poll.

| Provider | Batch call shape |
| :--- | :--- |
| Deepgram | **Synchronous.** `POST /v1/listen`; transcript in the response body — and it is the *only* chance to retrieve it. |
| AssemblyAI Sync | **Synchronous.** `POST https://sync.assemblyai.com/transcribe`; also returns `request_time_ms` so you can measure server time yourself. |
| ElevenLabs | **Synchronous.** `POST /v1/speech-to-text`. |
| AssemblyAI async | Submit `/v2/transcript`, then poll. Extra round trips. |
| Speechmatics | Job submit + poll. **Measured 12–22 s wall clock** — disqualifying. |

### 2.3 Per-provider notes

> [!WARNING]
> Three premises that circulate about this market are **stale or fabricated**,
> verified 2026-08-15. **There is no Deepgram Nova-4** — the catalog, changelog
> (latest entries 2026-08-05 and 2026-08-07, both Nova-3 language updates), and
> pricing page list only Flux / Nova-3 / Nova-2 / legacy / Whisper; a widely
> repeated "Nova-4, 7.4% WER, 180 ms" claim traces to `callsphere.ai` and appears
> nowhere on deepgram.com. **Deepgram Flux cannot do batch at all** — Deepgram's
> own matrix marks pre-recorded, smart formatting, and diarization as
> unsupported; it is WebSocket-only. And **AssemblyAI's Slam-1 is deprecated**,
> LeMUR was removed entirely in favour of a token-billed LLM Gateway, and
> ElevenLabs `scribe_v1` was deprecated with removal announced for 2026-07-09.

**Deepgram Nova-3 — fastest, cheapest to run, worst accuracy, worst default terms.**
427× real-time means inference on a 12 s clip is ~28 ms; your wall clock will be
TLS and upload, not compute. True per-second billing, free diarization on
pre-recorded, and a `dictation=true` parameter that converts spoken "comma",
"period", "new line" into real marks — nothing else here has an equivalent, and
it is free. `keyterm` (Nova-3 only) costs +30%. The catch is §3-shaped: the
published rates **opt you in** to the Model Improvement Program, and ToS §3.2
(updated 2026-08-06) grants an *irrevocable, perpetual, transferable,
sublicensable* licence over your content that **survives termination**.
`mip_opt_out=true` is a per-request query parameter available on every tier.
**Verdict: shortlisted, and `mip_opt_out=true` is non-negotiable.**

**AssemblyAI Sync API — purpose-built for exactly this workload.** Its docs name
"dictation and voice-to-text input" and "push-to-talk and voice commands" as the
primary use cases. One POST, transcript in the response, ~134 ms p50 claimed,
80 ms–120 s of audio, 16-bit mono 16 kHz WAV, `X-AAI-Model: universal-3-5-pro`.
3.0% WER — a full 2.2 points better than Deepgram. Two things make it the
strongest candidate: an unauthenticated no-op **`/warm` endpoint** you can fire
on hotkey-*down* so DNS + TCP + TLS overlap with the user speaking (watch idle
connection reaping — httpx drops at 5 s by default), and the **EU endpoint
`sync.eu.assemblyai.com`, which excludes you from model training at identical
price** on any tier, including free. That is the cleanest privacy story
available anywhere here without paying for it. **Verdict: adopt as the first
hosted backend if one is built.**

**ElevenLabs Scribe v2 — the accuracy ceiling, and the worst fit otherwise.**
2.2% WER is the best batch number on Artificial Analysis' board, at $0.22/hr.
But it is the slowest of the four (57× real-time), Zero Retention Mode is
Enterprise-only, and its billing granularity is the one fact in this section
nobody could verify — no general rounding statement exists in its pricing,
billing, or API docs. One correction worth carrying: the widely-cited claim that
self-serve users *cannot* opt out of training traces to a **Deepgram-authored
competitor page**; ElevenLabs' own privacy policy documents a self-serve opt-out
under Settings → Terms and Privacy → Data use. **Verdict: shortlisted only if a
future round shows accuracy is the binding constraint.**

**Speechmatics — best privacy posture, cheapest rate, disqualified by latency.**
Training is **opt-in and off by default** (worth 33% off if you take it — don't),
realtime audio is never stored, batch auto-deletes after 7 days or sooner via
API, custom dictionary is included at every tier, and US/EU/AU regions are
available on all tiers with SOC 2 Type II, ISO 27001:2022, GDPR, and HIPAA. It
is also the cheapest at $0.129/hr for Melia 1. And none of that survives the
measured numbers: novascribe's July 2026 run over 904 files put submit→transcript
wall clock at **12.1 s for Melia-1, 21.6 s Standard, 22.6 s Enhanced**. That is
job-queue overhead, not compute, and it is fatal for dictation.
**Verdict: rejected for this workload — revisit only for batch file
transcription, where it would be the best choice here.**

**AssemblyAI streaming — actively dangerous for push-to-talk.** It bills
WebSocket open-to-close wall time *including idle*, and an unclosed session
auto-closes at 3 hours and **bills all three**. **Verdict: rejected.**

**Self-hosting.** Deepgram's Docker/K8s path requires Enterprise, but the **AWS
SageMaker path needs only an AWS account** and runs fully airgapped with a
14-day unlimited trial — at the cost of per-request billing with a ~14 s
effective floor. Speechmatics' container, virtual appliance, and on-device
products are all Enterprise-only with no published pricing. AssemblyAI and
ElevenLabs are cloud-only.

**Custom vocabulary, compared.**

| Provider | Feature | Limit | Cost |
| :--- | :--- | :--- | :--- |
| Speechmatics | Custom dictionary | not published | **included, all tiers** |
| AssemblyAI | `keyterms_prompt` + natural-language `prompt` | async 1,000 words; **sync capped at 2,048 chars** | +$0.05/hr async; free on U3.5 Pro Realtime |
| ElevenLabs | `keyterms` | 1,000 terms, 50 chars each | +20%; >100 terms triggers a 20 s minimum billable duration |
| Deepgram | `keyterm` (Nova-3 only) | 100 terms, **hard cap 500 tokens**; docs advise 20–50 | +$0.078/hr (**+30%**) |

**Explicitly unverified**, and flagged so a later round does not mistake it for
settled: ElevenLabs' general billing granularity and free-tier STT hours;
whether `mip_opt_out=true` changes Deepgram's rate (the pricing page implies a
discount for opting in but publishes no second rate card); Deepgram's retention
period for MIP audio ("fractional increments" is not a duration); AssemblyAI's
Sync-specific billing basis and retention policy, which no page addresses; and
Speechmatics' Melia launch date, where the announcement says 2026-06-17 and the
changelog says 2026-07-14. Also note Gradium's May 2026 benchmark reports
Deepgram at 25.2% WER — ~5× every other source, in a vendor-run benchmark
Gradium wins. Treat as misconfigured.

### 2.4 Audio-native LLMs: a third category

Distinct from both "STT API" and "STT + LLM cleanup" is sending the audio
directly to a multimodal LLM and asking it for clean text. Both OpenAI
(`gpt-4o-audio-preview`, via `input_audio` content blocks in Chat Completions)
and Google (Gemini's audio understanding, including a native-audio Live variant)
support this (checked 2026-08-15;
[OpenAI](https://developers.openai.com/api/docs/models/gpt-4o-audio-preview),
[Gemini](https://ai.google.dev/gemini-api/docs/audio)). Mistral's Voxtral is
built around the same idea.

Why it's tempting: one round trip instead of two, and the model can apply
formatting instructions ("obey spoken commands like *new paragraph*"; "expand
spoken code identifiers") *with the acoustics still available* — so it can
disambiguate "there/their" from prosody rather than guessing from text.

Why it's not the v1 answer:

- **It is strictly the highest-variance option for the failure mode in §5.3.** A
  general-purpose LLM asked to "transcribe and tidy" has no separation between
  the transcription obligation and the editing licence. The whole mitigation
  toolkit in §5.4 (length-ratio guards, edit-distance guards, diffing against the
  raw transcript) depends on *having* a raw transcript to compare against. Fuse
  the steps and you throw away your only ground truth.
- Token-priced rather than audio-minute-priced, so cost is less predictable.
- No `Transcriber`-shaped fallback if the model decides to answer your dictation
  instead of transcribing it.

**Verdict: rejected for v1; genuinely interesting for v3.** If it is ever
adopted, run it *alongside* a cheap real transcription (Groq is nearly free) and
use the raw transcript as the guard rail.

---

## 3. Privacy and locality, stated plainly

This is the section to be blunt in, because the whole reason `mavor` exists locally
today is that dictation is an unusually invasive data stream.

### 3.1 What actually leaves the machine

**The audio.** Not "text you chose to send" — the raw microphone capture, which
contains:

- Everything you said, including the half-sentence you abandoned and the swear
  word before it.
- Whatever else the mic picked up: the other side of a phone call, a colleague
  in the room, a video playing on the machine.
- Content you would never have typed into a web form: credentials read aloud
  while debugging, client names, health details, unreleased plans, the contents
  of a private repo you're narrating.

**The daemon cannot tell the difference.** `mavor` has no idea whether the focused
window is a scratch buffer or a HIPAA-covered record. A hotkey is a very
low-friction way to make a network request, which is exactly what makes an
always-on cloud backend dangerous: the failure isn't a leak, it's a *habit*.

**With LLM post-processing, the transcript leaves too** — and if the post-processor
is hosted while the transcriber is local, the "local transcription" property is
worth nothing. Text is the sensitive part.

**Second-order exposure** worth naming: your **user dictionary**. The natural
implementation of jargon correction is to ship a list of proper nouns and
project-specific terms in the prompt on every call. That list is a compact,
high-signal dossier — colleague names, internal codenames, client names,
hostnames. It leaks continuously, on every dictation, whether or not you said any
of those words. Treat the dictionary as more sensitive than any individual
utterance.

### 3.2 What a sane opt-in design looks like

Five properties, roughly in order of importance:

1. **Never default to cloud.** A fresh install, a missing config file, and a
   malformed config all resolve to local whisper.cpp. `config.Default()` already
   does the right thing; keep it that way. A cloud backend must be *named* in
   config to be reachable.
2. **Fail closed, never fall back upward.** If the local backend errors, do not
   silently retry against the cloud. Falling back *down* (cloud unreachable →
   local) is fine and good; falling back *up* is a privacy violation dressed as
   resilience.
3. **Per-profile backends, bound to a hotkey.** This is the feature that makes
   cloud usable rather than merely available. Bind `$mod+grave` to the local
   profile and `$mod+shift+grave` to a `cloud-accurate` profile. The user then
   makes an explicit, per-utterance choice with muscle memory, and there is no
   ambient "is this thing phoning home right now?" question. This maps cleanly
   onto the existing IPC: `{"action":"toggle","profile":"cloud"}`.
4. **The overlay must show it.** The GTK pill already distinguishes Recording
   from Transcribing. A cloud profile should be *visually unmistakable* —
   different colour, and the provider name in the pill. If the user can't tell at
   a glance which backend is armed, per-profile backends are theatre. This is
   cheap to implement and is the highest-leverage safety feature in the list.
5. **Redaction, with honest expectations.** Two kinds, and they are not equally
   useful:
   - **Pre-send audio redaction is not practical.** You'd need to recognise the
     speech to know what to redact, which is the thing you were trying to avoid
     doing locally. Don't promise it.
   - **Post-transcription redaction before the *LLM* hop is practical and worth
     doing.** If transcription is local and only post-processing is hosted, a
     regex/entity pass over the transcript (emails, API-key shapes, long digit
     runs, a user deny-list of names) removes the worst material before it goes
     out. Several STT vendors also offer server-side PII redaction (Deepgram
     charges $0.002/min as an add-on; AssemblyAI includes it in Audio
     Intelligence — checked 2026-08-15) but **server-side redaction is
     security theatre for this threat model**: the unredacted audio already
     reached the vendor. It protects the *stored transcript*, not you.
   - A **local deny-list that aborts the send** ("if the transcript matches any
     of these patterns, do not upload, emit the local transcript instead") is
     more honest than redaction and easier to reason about.

Additional hygiene worth one line each: prefer providers offering a documented
no-training-by-default posture and a short retention window; prefer regional
endpoints if the user is in the EU; log which backend served each request so the
user can audit after the fact; never write audio to a path that outlives the
request (the current code writes a temp WAV — make sure the cloud path deletes it
on the same deferred path the local one does).

### 3.3 The honest summary

For a developer dictating commit messages and Slack replies, the cloud risk is
low and the accuracy/speed win is real. For the same developer dictating into a
customer's incident channel, it isn't. **The tool cannot make that judgement, so
the design's job is to make the choice explicit, per-utterance, and visible** —
not to pick a default and hope.

---

## 4. What post-processing is actually for

Post-processing is the largest quality lever available after the acoustic model,
and it is cheap to experiment with because it operates on text. But "run the
transcript through an LLM" is not one feature — it is six, with very different
risk profiles. Separating them is the main analytical point of this section.

| # | Job | Difficulty | Risk of meaning change | Notes |
| --- | --- | --- | --- | --- |
| 1 | Punctuation + casing repair | Easy | **Low** | Whisper already does this decently; the marginal win is small. Mostly needed for `tiny`/`base` models and for non-Whisper backends. |
| 2 | Filler / disfluency removal | Easy | **Low–medium** | "um", "uh", "you know", stutters, false starts. Medium risk because a "false start" and a self-correction look identical, and deleting the wrong half inverts the meaning ("go to Bombay, I mean Delhi"). |
| 3 | Jargon + proper-noun correction (user dictionary) | Easy | **Low** | The highest value-per-effort item for a developer. Often better solved *at the transcription layer* (see below). |
| 4 | Spoken formatting commands | Medium | **Medium** | "new paragraph", "make that a bullet list", "scratch that". Requires the model to distinguish command from content — "then I said scratch that" must survive. |
| 5 | Code-identifier handling | Medium–hard | **Medium** | "snake case foo bar" → `foo_bar`, "dot slash bin mavor" → `./bin/mavor`, "capital P capital R" → `PR`. Very high value for this user; poorly served by generic prompts. |
| 6 | Tone rewriting | Easy to invoke | **HIGH** | This is *supposed* to change the text, so none of the §5.4 guards apply. Keep it strictly separate from the repair pass — a different command, a different profile, never the default. |

**Two design conclusions fall out of the table:**

- **Job 3 mostly should not be an LLM job.** Every serious STT API has a
  vocabulary-biasing mechanism (Deepgram keyterm prompting, OpenAI's `prompt`
  parameter, AssemblyAI word boost, Speechmatics custom dictionary), and even
  local whisper-cli accepts an initial prompt. Biasing the *decoder* is strictly
  better than correcting the *output*: it costs no extra latency, no extra
  round-trip, and cannot rewrite anything it wasn't supposed to touch. Send the
  user dictionary to the transcriber first; only fall back to LLM correction for
  what leaks through. (Privacy caveat in §3.1 applies to the dictionary either way.)
- **Jobs 1–5 are one pass; job 6 is a different feature.** Bundling tone
  rewriting into the default cleanup prompt is how you get a tool that
  occasionally editorialises your commit messages. Ship repair by default (if at
  all) and rewriting as an explicit, separately-invoked mode.

---

## 5. Post-processing: local small LLM vs hosted

### 5.1 Local small models — the 2026 landscape

The relevant model classes for a ~1–4B, latency-sensitive, text-repair task
(all checked 2026-08-15):

| Family | Sizes | Released | License | Notes |
| --- | --- | --- | --- | --- |
| **Gemma 4** `E2B` / `E4B` | 2.3B / 4.5B *effective* | 2026-04-02 | Apache 2.0 | Explicitly edge-targeted, 128K context, native function calling, `ollama run gemma4:e2b` / `gemma4:e4b`. The "on-device default" per Ollama's own framing. ([Ollama library](https://ollama.com/library/gemma4), [guide](https://www.theaitechpulse.com/gemma4-ollama-guide-2026)) |
| **Qwen3.5 small** | 0.8B / 2B / 4B / 9B | 2026-03-02 | Apache 2.0 | 262K context, native vision. Artificial Analysis Intelligence Index: 9 (0.8B), 16 (2B), 27 (4B), 32 (9B) — note the **steep cliff below 4B**. ([Artificial Analysis](https://artificialanalysis.ai/articles/qwen3-5-small-models)) |
| **Phi-4-mini** | 3.8B | — | MIT | Reasoning/math lean; ~2.2 GB at Q4. Recommended as the best small reasoner for 8 GB machines. |
| **SmolLM3** | 3B | — | Apache 2.0 | Common CPU-only recommendation. |
| **Llama 3.2** | 1B / 3B | — | Llama license | 1B cited at 60+ tok/s on CPU; suited to "routing, classification, autocomplete" — i.e. probably too small for job 4/5 above. |

**Recommendation: start at `gemma4:e4b` or `qwen3.5:4b`, and treat 4B as the
floor, not the target.** The Qwen3.5 Intelligence Index numbers (27 at 4B vs 16
at 2B) show capability falling off a cliff below ~4B, and jobs 4 and 5 in the
table above are exactly the instruction-following tasks that fall off first. A
model that can restore punctuation but cannot reliably tell a formatting command
from content is not obviously better than no post-processing at all.

**Caveat on the evidence: this is the weakest-sourced recommendation in the doc.**
Public leaderboards in Aug 2026 don't track sub-8B models on instruction-following
(the IFEval leaderboard's smallest entry is LFM2.5-230M at 71.7%; the named small
models are absent —
[BenchLM IFEval](https://benchlm.ai/benchmarks/ifeval), checked 2026-08-15), and
none of the general benchmarks measure "repair this text without changing it".
This needs a local eval before it is trusted; see Open Question 3.

### 5.2 Local runtime: latency, and the two costs people forget

**Generation is not the whole cost.** For a 60-word input and 60-word output
(~80 tokens each), there are three components:

1. **Model load / cold start.** Ollama's default is to keep a model resident for
   **5 minutes** after the last request, then unload
   ([Ollama keep_alive](https://markaicode.com/ollama-keep-alive-memory-management/),
   checked 2026-08-15). For a dictation tool used in bursts, *the default is
   exactly wrong*: the first dictation after a coffee break pays a multi-second
   reload. Set `keep_alive: -1` (resident indefinitely) at daemon startup with a
   warm-up call, and accept the VRAM/RAM cost — or accept that the first
   dictation of each session is slow and say so in the overlay.
2. **Prefill (prompt processing).** A cleanup system prompt plus a user
   dictionary is a stable prefix re-processed on every call unless cached. A
   4096-token prefix has been measured at ~410 ms of pre-first-token overhead on
   llama.cpp, dropping to <50 ms with prefix caching
   ([CraftRigs](https://craftrigs.com/guides/llama-cpp-server-prefix-cache-setup-verify/),
   checked 2026-08-15). **This is the single highest-leverage local optimization**
   and it is free: `llama-server` reuses KV cache across requests via slot
   matching (default similarity threshold 0.5) and supports host-memory prompt
   caching
   ([llama.cpp #20574](https://github.com/ggml-org/llama.cpp/discussions/20574),
   [#13606](https://github.com/ggml-org/llama.cpp/discussions/13606)). Design the
   prompt so the dictionary sits in the *stable prefix* and only the transcript
   varies at the end.
3. **Decode.** The actual generation. ~80 output tokens.

Rough end-to-end for ~80 output tokens, with the model resident and the prefix
cached:

| Hardware | Realistic wall time | Confidence |
| --- | --- | --- |
| Consumer GPU (RTX 3060/4070-class), 4B @ Q4 | **~0.3–0.8 s** | Medium — inferred from typical 50–150 tok/s decode plus cached prefill |
| Apple Silicon, 4B @ Q4 | **~0.4–1.0 s** | Medium |
| CPU-only, 4B @ Q4 | **~15–30 s — not viable** | Medium-high. One 2026 survey puts small models at 3–6 tok/s at Q4 on modern CPUs and calls it "non-interactive" ([Local AI Master](https://localaimaster.com/blog/small-language-models-guide-2026)) |
| CPU-only, 1B @ Q4 | ~1.5–3 s (60+ tok/s cited for Llama 3.2 1B) | Low — and a 1B model is likely too weak for jobs 4–5 |

**These are estimates, not measurements** — they are assembled from tok/s figures
rather than measured on this task. Benchmark before committing. The important
qualitative finding is robust, though:

> **Local LLM post-processing is a GPU feature.** On a CPU-only machine it is
> either non-interactive (4B) or too dumb to be worth the latency (1B). Since
> whisper.cpp itself already wants the GPU, a machine that can do local
> post-processing well is a machine that didn't need hosted STT in the first
> place. This is the central tension in the whole design.

**Runtime choice for a Go daemon:** Ollama. It has a first-party Go client
(`github.com/ollama/ollama/api`, with `KeepAlive: &api.Duration{...}`), a stable
HTTP API, and handles model pulls — which matters because `mavor models pull`
already exists as a UX pattern for whisper models and extends naturally.
`llama-server` gives finer control over prefix caching and grammars, at the cost
of the daemon owning process supervision. **Start with Ollama; drop to
`llama-server` only if prefix-cache control proves to be the bottleneck.**

### 5.3 Hosted post-processing

If the transcript is already leaving the machine, a hosted small model is
strictly better than a local one on quality and comparable on latency.

**Claude Haiku 4.5** is the relevant tier (all figures checked 2026-08-15; the
model catalogue is from a cached vendor-reference table, dated 2026-06-24,
and corroborated by search — the live docs endpoint returned 502 during this
round, so re-verify):

- **Model ID:** `claude-haiku-4-5` (full ID `claude-haiku-4-5-20251001`). This is
  still the cheapest/fastest Claude tier — **there is no Haiku 5 as of
  2026-08-15**; the 5-series so far is Opus 5, Sonnet 5, Fable 5, Mythos 5.
- **Price:** **$1.00 / MTok input, $5.00 / MTok output.** 200K context, 64K max
  output.
- **Latency:** ~0.66 s time-to-first-token, ~90 tok/s
  ([pricepertoken](https://pricepertoken.com/pricing-page/model/anthropic-claude-haiku-4.5) —
  page 403'd on direct fetch, figure via search snippet, **treat as soft**).

**Cost per dictation.** Assume an 800-token system prompt (instructions + user
dictionary), a 100-token transcript, and a 100-token result:

```
input:  900 tok × $1.00/MTok = $0.00090
output: 100 tok × $5.00/MTok = $0.00050
                              ---------
                     per call ≈ $0.0014   (~0.14¢)
```

100 dictations/day ≈ **$4.20/month**. That is ~1000× the transcription cost —
**the post-processing pass, not the transcription, is where hosted money goes.**

**Latency per dictation:** ~0.66 s TTFT + ~100 tok ÷ 90 tok/s ≈ 1.1 s + network,
so call it **1.2–1.5 s**. And note: **streaming does not help here.** The daemon
needs the complete string before it can call `wtype`. Every token of the
post-processor's output is on the critical path in a way that a chat UI's isn't.

**Two gotchas specific to this workload:**

1. **Prompt caching will silently not work.** The minimum cacheable prefix on
   Claude Haiku 4.5 is **4096 tokens** (per the vendor prompt-caching
   reference). An 800-token system prompt is far below that: you get
   `cache_creation_input_tokens: 0`, no error, and no savings. Either accept
   uncached pricing (it's $4/month, so — accept it) or, if you ever do pad the
   prefix past 4096 tokens, note that the minimum is **not monotonic across
   generations** (512 on Opus 5, 1024 on Sonnet 5, 4096 on Haiku 4.5), so a model
   swap can silently turn caching off.
2. **Prefer a cheap, fast, *dumb enough* model.** The §5.4 failure mode gets
   worse, not better, with model capability: a more capable model is more
   confident about "improving" your text. Haiku-tier is the right tier for repair
   work on both cost and behaviour grounds.

Alternatives at this tier worth pricing if Claude is not preferred: Groq's
LLM endpoints (same API key as the transcription backend, which is a real
operational simplification), or a local model via the same OpenAI-compatible
shape. Not researched in depth this round.

### 5.4 The failure mode: rewriting meaning instead of repairing text

This is the reason post-processing must be opt-in and guarded, and it is
well-attested in the literature rather than folklore.

**What goes wrong.** An LLM asked to clean a transcript is doing constrained
generation with no hard constraint. Observed and documented behaviours:

- **Hallucinated insertions.** "Autoregressive models exhibit high insertion
  rates, suggesting potential hallucinations when acoustic evidence is weak"
  ([NLE, arXiv 2603.08397](https://arxiv.org/pdf/2603.08397)). Weak acoustic
  evidence is exactly what a mumbled dictation is.
- **Silent paraphrase.** The output is fluent, plausible, and not what you said.
  This is the worst case because it survives a glance.
- **Answering instead of transcribing.** Dictate a question and a naive prompt
  gets you an answer.
- **Over-deletion of self-repairs.** Even a *purpose-trained* cleanup model gets
  this wrong: FluentWhisper (below) corrects self-repairs only ~40% of the time
  and drops repeated digits in ~80% of cases — its author explicitly warns it is
  "not suitable for content where dropped numbers matter (phone numbers, account
  IDs)".
- **Cost scaling with the wrong thing.** Full-rewrite decoding cost grows with
  output length regardless of how much of the input was already correct
  ([arXiv 2501.13831](https://arxiv.org/html/2501.13831)). You pay full price to
  re-emit text that needed no change — and every re-emitted token is a token that
  could come out different.

**Mitigations, ordered by effectiveness:**

1. **Make the model emit edits, not a rewrite.** This is the strongest
   structural fix and it is a live research direction: compact phrasal-edit and
   "target-only" representations recover 50–60% of the full-rewrite quality gap
   while keeping 70–80% of the efficiency gain, with a 99.80% successful-expansion
   rate ([arXiv 2501.13831](https://arxiv.org/html/2501.13831), checked
   2026-08-15). Applying edits mechanically means the model *cannot* change a
   span it didn't name. This is more implementation work than a rewrite prompt
   and is the right long-term design.
2. **Guard rails on the output, applied unconditionally.** Cheap, and they catch
   the catastrophic cases:
   - **Length-ratio guard.** If `len(clean)` is outside ~[0.6×, 1.3×] of
     `len(raw)`, discard and emit `raw`. (Asymmetric: filler removal legitimately
     shortens; almost nothing legitimately lengthens.)
   - **Edit-distance / token-overlap guard.** If normalized Levenshtein distance
     exceeds a threshold, discard and emit `raw`.
   - **Content-word retention.** Every non-stopword in `raw` should appear in
     `clean` unless it's a known filler or a dictionary substitution. A dropped
     proper noun is a silent data loss.
   - **Numeral preservation.** Every digit run in `raw` must survive into
     `clean`. This is the FluentWhisper failure and it is exactly the one that
     hurts a developer (ports, versions, error codes).
3. **Constrained decoding.** llama.cpp GBNF grammars and Ollama structured
   outputs can force the output into an edit-list JSON schema, which composes
   with mitigation 1. Not a semantic guarantee — a grammar constrains *shape*,
   not *truthfulness* — but it makes the edit-list design robust to format drift.
4. **Prompt hygiene.** Lowest-leverage but free: state the contract as *repair*,
   forbid answering, forbid adding, give the dictionary as data not instruction,
   and add explicit negative cases. Do not rely on this alone.
5. **Always keep the raw transcript.** Put the unmodified transcript on the
   clipboard (or in a log the user can retrieve) even when the cleaned version is
   typed. `wl-copy` is already in the pipeline. This turns a silent corruption
   into a recoverable one and is close to free.

**The invariant to design around:**

> Post-processing may **delete** fillers, **fix** casing and punctuation, and
> **substitute** dictionary terms. It may not **add** content words, and every
> numeral must survive. Anything else, discard the result and emit the raw
> transcript.

Encoding that as executable guards is more valuable than any amount of prompt
tuning, and it is the difference between an opt-in feature and a footgun.

### 5.5 The alternative: fold cleanup into the acoustic model

**FluentWhisper** (checked 2026-08-15) is a LoRA adapter on
`whisper-large-v3-turbo` that emits already-cleaned text in a single pass —
removing filled pauses, discourse markers ("you know", "I mean"), repetitions
("the the server"), and self-repairs. Apache-2.0. Reported 3.42% WER vs vanilla
Whisper's 9.42% on its test set (+6 points, 95% CI [+5.0, +7.0]).
([HF blog](https://huggingface.co/blog/pradachan/fluent-whisper))

This is architecturally attractive for `mavor`: **zero added latency, zero added
processes, and no second model to keep resident** — it covers jobs 1 and 2 from
§4's table for free. It does not cover jobs 3–6.

Caveats from its own author, and they are serious: evaluated on **one speaker
reading scripted lines**, so generalization to spontaneous speech and other
accents is unestablished; drops repeated digits ~80% of the time; only fixes
self-repairs ~40% of the time.

**Verdict: watch, and evaluate.** If it holds up on real dictation it removes
the need for a post-processing LLM for the two lowest-risk, highest-frequency
jobs, which is a much better trade than adding a second model to the pipeline.
Deeper evaluation belongs in the sibling open-weight-models doc; flagged here
because it competes directly with this layer.

---

## 6. Streaming / realtime, and whether it changes the interaction model

### 6.1 The architectural argument (provider-independent)

Streaming's win for push-to-talk dictation is specific and easy to state:
**it moves transcription off the critical path by overlapping it with speaking.**

```
BATCH:      [ ——— speaking ——— ][ upload ][ transcribe ][ type ]
                                 └──────── user waits ────────┘

STREAMING:  [ ——— speaking ——— ][ tail ][ type ]
             └ transcribed as you go ┘   └ user waits ┘
```

The user's perceived latency stops being "transcribe the whole clip" and becomes
"finalize the last chunk" — the *tail*. For a 30-second dictation the difference
is large; for a 5-second one it is small, because there wasn't much to overlap.

Three second-order effects matter as much as the tail latency:

1. **Perceived latency drops even further if you show interim results.** The
   overlay currently shows a spinner during Transcribing. A streaming backend
   could show the words arriving. Even if total time were identical, the wait
   *feels* shorter and the user gets an early warning when the mic is picking up
   nothing — which today is only discoverable after the full round trip.
2. **It enables a hands-free mode.** Providers that expose end-of-turn or
   semantic-VAD detection let you drop the second keypress: speak, stop, text
   appears. That is a genuinely different interaction model, not just a faster
   version of the current one. It is also a change the current FSM
   (Idle ⇄ Recording ⇄ Transcribing, driven purely by `EventToggle`) does not
   model — you'd need a Recording→Transcribing transition with a non-IPC source.
3. **It costs more, per §6.2, and it is more code.** Streaming endpoints price at
   a premium over batch, and the daemon has to hold a websocket, handle
   mid-utterance disconnects, and reconcile interim vs final results.

### 6.2 Provider landscape and numbers

<!-- STREAMING-PROVIDERS -->

### 6.3 Verdict

**Rejected for v1; the right v2 feature.** The reason is architectural, not
economic. Today's pipeline is
`Recorder.Start → [hotkey] → Recorder.Stop() → wavPath → Transcribe(path) → string`.
Streaming replaces that with a duplex flow where audio frames go out while text
comes back, which means:

- `audio.Recorder` must grow a "give me frames as they arrive" mode alongside
  `Stop() (wavPath, error)`. `parec` writing to a file is the wrong shape; you
  want it writing to a pipe the daemon reads.
- `speech.Transcriber`'s `Transcribe(ctx, wavPath) (string, error)` cannot express
  it at all. Streaming needs a second interface, not an implementation of this one.
- The FSM gains states (or at least, Transcribing stops meaning "nothing is
  happening yet").
- The overlay grows a text-rendering path for interim results.

That is a multi-package change touching the recorder, the transcriber, the FSM,
and the overlay — for a latency win that, on *short* utterances, is measured
against a batch path that is already only a few hundred milliseconds. **Get the
batch hosted backend right first; it is a one-file change and captures most of
the accuracy benefit.** Revisit streaming when either (a) users report the wait
is the main complaint, or (b) hands-free mode becomes a goal, at which point
streaming is a prerequisite rather than an optimization.

---

## 7. What this means for the codebase

**The `Transcriber` interface is already right, with one wrinkle.**
`Transcribe(ctx, wavPath) (string, error)` fits any batch HTTP backend cleanly.
The wrinkle is that it takes a *path*, which forces a hosted implementation to
re-read the file it's about to POST — fine, but it also means a future streaming
backend cannot implement this interface at all. Leave it as-is for the hosted
work and treat streaming as a separate interface when it comes (§6).

**Config is the part that needs to change.** Today:

```toml
model     = "base.en"                     # whisper-specific
model_dir = "~/.cache/mavor/models"       # whisper-specific
```

Flat, single-backend, whisper-shaped. The per-profile design from §3.2 wants
something like:

```toml
default_profile = "local"

[profiles.local]
backend = "whisper-cli"
model   = "base.en"

[profiles.cloud]
backend      = "groq"
model        = "whisper-large-v3-turbo"
api_key_env  = "GROQ_API_KEY"      # env var name, never the key itself
dictionary   = ["wlroots", "PipeWire", "gtk4-layer-shell"]
postprocess  = "none"
```

Two notes. **Never put the API key in the TOML** — reference an env var or a
path, so the config file stays shareable and greppable. And a profile is the
natural place to hang the post-processing choice, which keeps "cloud
transcription + local cleanup" and "local transcription + no cleanup"
independently expressible.

**A `PostProcessor` interface belongs next to `Transcriber`**, with the same
Mock-for-tests shape, a `Noop` default, and the §5.4 guards implemented in the
*wrapper* rather than in each backend — so no implementation can skip them:

```go
type PostProcessor interface {
    Process(ctx context.Context, raw string) (string, error)
}
// Guarded wraps a PostProcessor and falls back to raw on any guard violation.
func Guarded(p PostProcessor, g ...Guard) PostProcessor
```

**Daemon changes are small.** `runTranscription` gains one step between
`Transcribe` and `output.Emit`, and it must be non-fatal: a post-processing
failure emits the raw transcript rather than aborting the cycle. That mirrors the
existing treatment of `output.Emit` errors, which is already the right instinct.

**Testing.** The `Mock` pattern extends directly. The interesting tests are the
guards (table-driven: raw/clean pairs and the expected accept/reject), which need
no network and no model — that is a large fraction of the risk covered by fast
unit tests.

---

## 8. Fast-moving — verify before building

Everything in this section was checked on **2026-08-15** and will rot. Re-check
before writing code against any of it.

### Prices (all per audio hour unless noted)

<!-- FASTMOVING-PRICES -->

### Post-processing model facts

| Fact | Value as of 2026-08-15 | Where to re-check |
| --- | --- | --- |
| Cheapest/fastest Claude tier | `claude-haiku-4-5`, $1.00/$5.00 per MTok, 200K ctx, 64K max out. **No Haiku 5 exists yet.** | cached vendor reference; `platform.claude.com/docs/en/about-claude/models/overview.md` (was 502 on 2026-08-15) |
| Claude Haiku 4.5 latency | ~0.66 s TTFT, ~90 tok/s | soft — vendor-adjacent third party; measure |
| Claude Haiku 4.5 min cacheable prefix | **4096 tokens** (non-monotonic across models: 512 on Opus 5, 1024 on Sonnet 5) | cached vendor reference → prompt-caching documentation |
| Prompt cache economics | read ~0.1×, write 1.25× (5m TTL) / 2× (1h TTL) | same |
| Gemma 4 | Released 2026-04-02, Apache 2.0. `E2B` = 2.3B eff., `E4B` = 4.5B eff., 128K ctx. Also 26B-A4B MoE and 31B dense. | ollama.com/library/gemma4 |
| Qwen3.5 small | Released 2026-03-02, Apache 2.0, 262K ctx. Intelligence Index 9/16/27/32 for 0.8B/2B/4B/9B. | artificialanalysis.ai |
| Ollama `keep_alive` default | 5 minutes, then unload. `-1` = resident forever. | Ollama docs |
| llama.cpp prefix cache | Slot-matching default similarity 0.5; host-memory prompt caching available; ~410 ms → <50 ms TTFT on a 4096-token prefix | llama.cpp discussions #20574, #13606 |

### Things most likely to have changed by the next round

1. **A Haiku 5.** The 5-series shipped Opus/Sonnet/Fable/Mythos and skipped
   Haiku; a cheap fast 5-tier would change §5.3's numbers and possibly its
   verdict.
2. **STT model names.** `gpt-transcribe` superseded `gpt-4o-transcribe` as
   OpenAI's recommended file-transcription model within a few months. Assume the
   name you hardcode will be stale in six months and make it config, not a
   constant.
3. **Billing increments.** Cheap to re-verify, expensive to model wrong.
4. **Small-model generation.** Qwen3.6 exists at larger sizes as of 2026-08;
   small variants presumably follow. Gemma 5 likewise.

---

## Open Questions

These need a decision from the project owner; research can inform but not settle
them.

**1. What is the privacy contract this tool promises?**

`mavor` today has a simple, strong, marketable property: *nothing leaves the
machine*. Adding a hosted backend spends that property permanently — even
off-by-default, the answer to "does this send my voice to the cloud?" changes
from "no" to "it depends how you configured it", and that is the answer a
security-conscious user has to audit rather than trust. The mitigations in §3.2
(never-default, fail-closed, per-profile, visible-in-overlay) make it defensible,
but they do not make it "no".

_Leaning:_ Ship it, but make the promise precise and put it in the README rather
than leaving it implied: "local by default, cloud only for profiles you name, and
the overlay always tells you which is active." Implement the overlay indicator in
the same commit as the first hosted backend, not after — it is the load-bearing
piece and it will never get done later.

> **Answer:**

**2. Should post-processing ship at all in v1, and if so on by default?**

The strongest argument against: §5.4's failure mode is real, the guards are
non-trivial, and whisper `base.en`+ already punctuates acceptably — so job 1 (the
easy, safe win) is mostly already done, and the remaining jobs are the risky
ones. The strongest argument for: jobs 4 and 5 (formatting commands, code
identifiers) are the difference between "transcription" and "dictation" for a
developer, and nothing else provides them.

_Leaning:_ Ship the `PostProcessor` interface and the guards in v1 with `Noop` as
the default, and ship exactly one real implementation aimed at jobs 4–5 (spoken
commands + code identifiers), because those are the ones with no alternative.
Push job 3 (jargon) down to decoder biasing where it belongs, and leave jobs 1–2
to the acoustic model unless measurement says otherwise. **Never on by default.**

> **Answer:**

**3. What hardware is the target machine, and does it have a GPU to spare?**

§5.2's conclusion is uncomfortable: local post-processing needs a GPU, and so
does local whisper. On a GPU-equipped machine, local-everything is viable and
hosted STT is only about accuracy. On a CPU-only machine, hosted STT is very
attractive and local post-processing is off the table — meaning the *only*
post-processing option is hosted, which is exactly the configuration with the
worst privacy story. The answer determines whether "local transcription + local
cleanup" is a real configuration or a fiction.

_Leaning:_ Assume a GPU-equipped Linux desktop (the Sway/wlroots audience skews
that way) and design so the CPU-only path degrades to "hosted STT, no
post-processing" rather than to something slow.

> **Answer:**

**4. Is a dictionary/keyterm mechanism worth choosing a provider for?**

§4 argues decoder biasing beats output correction. Providers differ a lot here:
Deepgram's keyterm prompting is a real feature with real accuracy claims;
OpenAI's `prompt` parameter is a weaker, less predictable hint. If "it keeps
mis-hearing my project's jargon" is the actual daily pain, that should dominate
provider choice over price and even latency.

_Leaning:_ Prototype with Groq (cheap, fast, easy) and measure jargon error rate
on a real corpus of your own dictations before paying Deepgram's premium. Do not
choose on the feature matrix; choose on your own WER.

> **Answer:**

---

## Sources / See also

All links checked **2026-08-15** unless noted. One line of *why* per source.

**Pricing and billing terms**

- [Deepgram pricing](https://deepgram.com/pricing) — the per-second-billing FAQ quoted in [§2.1](#21-the-billing-increment-trap), and the footnote revealing that listed rates opt you into model training.
- [Deepgram Terms of Service](https://deepgram.com/terms) — §3.2 (updated 2026-08-06) is the perpetual, irrevocable content licence that survives termination.
- [AssemblyAI pricing](https://www.assemblyai.com/pricing) and [billing docs](https://www.assemblyai.com/docs/billing-and-pricing) — per-second pro-rating, and confirmation the EU region costs the same as US.
- [Speechmatics pricing](https://www.speechmatics.com/pricing) — browser-rendered (the table is JS-only), stamped "Last updated 31 July 2026"; the ~40–65% price cut that stale third-party trackers still miss.
- [ElevenLabs API pricing](https://elevenlabs.io/pricing/api) — Scribe v2 rates; notable for what it does *not* say about billing granularity.

**Latency and accuracy measurements**

- [Artificial Analysis — speech to text](https://artificialanalysis.ai/speech-to-text) — the AA-WER v2 and speed-factor figures in the comparison table; note its Speechmatics prices are stale.
- [Artificial Analysis — streaming](https://artificialanalysis.ai/speech-to-text/streaming) — time-to-final-transcript numbers (Deepgram Flux 0.02 s, ElevenLabs Scribe v2 Realtime 0.14 s).
- [novascribe: how accurate is Speechmatics](https://novascribe.ai/how-accurate-is-speechmatics) — the 904-file July 2026 run whose 12–22 s wall-clock timings disqualify Speechmatics batch for dictation.
- [Gradium STT benchmark](https://gradium.ai/content/stt-api-benchmark-2026-latency-accuracy) — **cited as a dead end**: reports Deepgram at ~25% WER, ~5× every other source, in a benchmark its own author wins.

**API mechanics worth reading before implementing**

- [AssemblyAI Sync speech-to-text](https://www.assemblyai.com/products/sync-speech-to-text) — the product page that names push-to-talk dictation as the target use case.
- [AssemblyAI connection pre-warming](https://www.assemblyai.com/docs/sync-stt/connection-pre-warming) — the `/warm` endpoint and the idle-connection-reaping caveat.
- [Deepgram pre-recorded audio](https://developers.deepgram.com/docs/pre-recorded-audio) — confirms the batch call is synchronous and the response is the only chance to get the transcript.
- [Deepgram `dictation` parameter](https://developers.deepgram.com/docs/dictation) — spoken "comma"/"period"/"new line" → real punctuation, free.
- [Deepgram `keyterm`](https://developers.deepgram.com/docs/keyterm) — the 500-token hard cap that makes a large user dictionary impossible.
- [Flux vs Nova-3 comparison](https://developers.deepgram.com/docs/flux/flux-nova-3-comparison) — the matrix proving Flux has no pre-recorded mode.

**Privacy, retention, and training**

- [Deepgram Model Improvement Partnership](https://developers.deepgram.com/docs/the-deepgram-model-improvement-partnership-program) — what `mip_opt_out=true` actually changes.
- [AssemblyAI data retention and model training](https://www.assemblyai.com/docs/data-retention-and-model-training) — free users cannot opt out, but the EU endpoint excludes them anyway.
- [Speechmatics plans](https://docs.speechmatics.com/administration/plans) — training opt-in-by-default-off, the 33% discount for opting in, and 7-day batch deletion.
- [ElevenLabs privacy policy](https://elevenlabs.io/privacy-policy) — documents the self-serve training opt-out that a competitor page claims does not exist.
- [ElevenLabs Zero Retention Mode](https://elevenlabs.io/docs/eleven-api/resources/zero-retention-mode) — Enterprise-gated, which is the real limitation.

**Self-hosting**

- [Deepgram on Amazon SageMaker](https://developers.deepgram.com/docs/amazon-sagemaker) — the only non-Enterprise airgapped path here, and the source of the ~14 s per-request billing floor.
