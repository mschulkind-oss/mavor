---
title: "Naming: how the dictation daemon became mavor"
author: "Matthew Schulkind"
date: 2026-09-03
status: accepted
tags: [naming, release, open-source, branding]
summary: "The name search that settled on mavor: the criteria, the shortlist drawn from the history of shorthand and verbatim transcription, the availability checks, and every rejection with its reason."
---

# Naming: how the dictation daemon became `mavor`

> [!NOTE]
> **Decided: `mavor`.** The project shipped under the working title `stt` until
> 2026-09-03. This document is the search that replaced it, kept for the
> reasoning and the rejections rather than as an open question.

`mavor` is a low-latency voice-to-text dictation daemon and CLI: hold a key,
speak, and the words are typed into whatever window has focus. It runs
in-process Sherpa-ONNX and whisper.cpp engines over a PipeWire capture stream,
streams tokens live into a GTK4 HUD overlay, and emits the result as synthetic
keystrokes.

**William Mavor**'s *Universal Stenography* (1780) went through edition after
edition and taught shorthand to a generation of English schoolchildren — a
schoolmaster's system, meant to be learned quickly and used every day. Five
characters, spelled right on first hearing, clear on PyPI, npm and crates.io,
no collision with any command in the major distro package indexes, and a GitHub
namespace with 75 repositories and nothing in it.

"Speech-to-text" survives as the descriptive tagline — "`mavor`, a low-latency
speech-to-text dictation daemon" — so nothing in the documentation loses its
search terms. Nothing in the tree carries the old name: the engine package that
briefly kept it is `internal/speech`, because a package prefixes its own errors
and an acronym cannot be defined inside an error prefix.

## What the name has to do

1. **Be a name, not a description.** Compounds built from the stack it happens
   to target — the compositor, the display protocol, the acronym — describe an
   implementation detail and date badly. **Names containing `sway` or `way` are
   out of scope**, along with `vox`/`stt`/`speech` mashups. Nothing about the
   product is Wayland-specific from the user's side: you hold a key and speak.
2. **Be short and easy.** Four to six characters, typed without thinking,
   spelled correctly on the first hearing. It is typed constantly — `<name>
   toggle`, `<name> daemon`, `<name> status`, and inside a keybind.
3. **Be clear on PyPI.** *Decided, not preferred.* Every candidate below is
   available on PyPI; anything squatted there has been cut, including names
   that were otherwise leading.
4. **Not collide with an existing command** in any major distro, and not share
   its name with an established project in the audio, speech, or
   developer-tooling space.
5. **Have a story worth telling in one sentence.** A README's first line is
   cheaper than a logo.

The well is **the history of capturing speech verbatim as it is spoken** —
shorthand systems, the people who invented them, and the marks they wrote with.
A second, looser vein sits below it: words for the copy, the mark, and the
mechanism of hearing.

## How availability was checked

| Column | Method | What a miss costs |
|---|---|---|
| **PyPI** | Registry API lookup | **Blocking, by decision.** Not shipped there today, but a squat forecloses ever publishing under the project's own name. |
| **Cmd** | [command-not-found.com](https://command-not-found.com), which indexes binary names across Debian, Ubuntu, Arch, Fedora, Alpine, and Homebrew | **Blocking.** A collision shadows or is shadowed by a binary already in `$PATH`. |
| **GH** | GitHub repo search by name, sorted by stars, read for a notable project in an adjacent domain | **Near-blocking.** Sharing a name with an established neighbour is a permanent SEO and support tax. |
| **npm / Cargo** | Registry API lookups | **Cosmetic.** A Go binary and a Go module; the module path is `github.com/mschulkind-oss/<name>`, which is ours to take. |

The GitHub *username* column from earlier revisions is gone: every single-word
English name is squatted by a dormant account, so it returned `Taken` for all
fifty candidates and discriminated nothing. The repo path under
`mschulkind-oss` is the availability that matters, and it is free for every name
here.

## Finalists

All PyPI-clear, all free of command collisions.

| Name | Len | PyPI | npm | Cargo | GH namespace |
|---|:---:|:---:|:---:|:---:|---|
| **`byrom`** | 5 | Avail | Avail | Avail | Virgin — 22 repos, top 2★ |
| **`gregg`** | 5 | Avail | Avail | Taken | Noisy — 1.1k repos, mostly the given name |
| **`mavor`** | 5 | Avail | Avail | Avail | Virgin — 75 repos, top 16★ |
| **`pepys`** | 5 | Avail | Avail | Taken | Quiet — one 35★ journal app |
| **`notae`** | 5 | Avail | Avail | Avail | Quiet — 136 repos, all Portuguese school-grading |
| **`siglum`** | 6 | Avail | Avail | Avail | Virgin — 12 repos, top 8★ |
| **`pitman`** | 6 | Avail | Avail | Avail | Quiet — surname noise only |
| **`platen`** | 6 | Avail | Taken | Taken | One 51★ docs-authoring toolkit |
| **`teeline`** | 7 | Avail | Avail | Avail | Virgin — 15 repos, shorthand teaching toys |

### `byrom`

**John Byrom** (1692–1763) invented a shorthand system and then sold it as a
secret. Subscribers paid to be taught "The Universal English Short-hand" and
agreed not to pass it on; John and Charles Wesley learned it, and Byrom's own
journals — decades of conversation written down as it happened — are in it.

Five characters, one syllable-and-a-half, no spelling ambiguity, and it reads as
a person's name rather than an abbreviation: `byrom start`, `byrom toggle`,
`byrom history -n 5`. Free on PyPI, npm, and crates.io. Twenty-two repositories
on GitHub carry the name and the largest has two stars.

This is the closest thing in the search to the name that was cut: a real person,
short, and the story is one sentence long.

### `gregg`

**John Robert Gregg** published his system in 1888, and for most of the
twentieth century "taking Gregg" simply meant taking dictation in America. The
most-learned shorthand in history, and the only name here most people will
already half-recognise.

Five characters, free on PyPI and npm, taken on crates.io. Two caveats: Gregg is
an extremely common given name and surname, so the GitHub namespace is a
thousand repositories of unrelated people, and the one thematic neighbour is a
76★ Gregg Shorthand dictionary — small enough to be harmless, and it points at
the reference rather than away from it.

### `mavor` — chosen

The reference is in the header above. What decided it against the rest of this
list: five characters, one unambiguous spelling from one hearing, clear on all
three registries, and 75 repositories on GitHub with nothing in any of them.

The weakness is that it carries no signal on its own — it reads as an invented
product name until you are told otherwise. That is also, on balance, its
strength: a name nobody can parse is a name nobody misreads as a description.

### `pepys`

**Samuel Pepys** wrote his diary — a decade of overheard talk, gossip, and things
said in rooms — in Thomas Shelton's shorthand, and nobody could read it for 150
years. The most famous act of private verbatim recording in English.

Five characters, free on PyPI and npm, taken on crates.io, one adjacent
neighbour (a 35★ Markdown journal app). The problem is the mouth, not the
registry: it is pronounced *peeps*, and roughly nobody gets that right on sight.

### `notae`

*Notae Tironianae* — Tironian notes — is the actual name of the first shorthand
system in the Western record, invented by Cicero's secretary to keep up with a
speaker in real time. The Tironian *et*, `⁊`, is still in Unicode and still on
Irish road signs.

Five characters, free on all three registries. It is the most on-brand name in
the document and it has one real flaw: it is a single keystroke away from
`notes`, and users will type `notes` by reflex forever.

### `siglum`

A **siglum** is the scribal abbreviation mark that stands in for a whole word,
and, in textual criticism, the single letter that stands for an entire
manuscript. The unit of compression that made shorthand possible — the closest
thing a manuscript had to a token.

Six characters, free everywhere, twelve repositories on GitHub. It looks like a
product name without being one, and it is the most abstract of the finalists: it
names a mechanism rather than a person or a system.

### `pitman`

Isaac Pitman's shorthand, published in 1837, is still taught. Clean on every
registry, no meaningful GitHub footprint, six characters. The reservation is
that it reads as an ordinary surname with no signal — and a pitman is also a
connecting rod and a coal miner. Correct reference, thin evocation, and `mavor`
does the same job with a quieter namespace.

### `platen`

The **platen** is the roller in a typewriter, and the plate in a press, that the
type strikes against — the surface the letters land on. This program's last
stage is synthetic keystrokes landing in whichever window has focus, which makes
the focused window the platen.

Free on PyPI; npm and crates.io are taken, and `platenio/platen` is a 51★
documentation-authoring toolkit — adjacent, not conflicting. Mechanical and
concrete, but it names the last inch of the pipeline rather than the act.

### `teeline`

**Teeline** is the shorthand British journalists learn, designed in 1968 for
exactly these working conditions: speech arriving faster than it can be written,
captured live, transcribed immediately. Still the standard for UK press
accreditation.

Free on all three registries with a fifteen-repository footprint, and the only
finalist that looks like a finished product name to someone who has never heard
of a shorthand system — which is nearly everyone. Two characters longer than the
brief asks for.

## Second tier

Checked, PyPI-clear, kept in reserve. The first group is on-brand; the second is
the looser vein — the copy, the mark, and the mechanism of hearing.

| Name | Len | PyPI | npm | Cargo | The reference |
|---|:---:|:---:|:---:|:---:|---|
| **`pernin`** | 6 | Avail | Avail | Avail | Pernin's Universal Phonography, an American light-line system of the 1880s. Twenty-eight repos on GitHub, none of them anything. |
| **`shelton`** | 7 | Avail | Avail | Avail | The system Pepys actually wrote in. Clean everywhere; reads as pure surname. |
| **`stolze`** | 6 | Avail | Avail | Avail | Wilhelm Stolze's German shorthand. Clean sweep; English speakers will not know where the stress goes. |
| **`arends`** | 6 | Avail | Avail | Avail | Leopold Arends' German system. Clean sweep, reads as a plural. |
| **`schrey`** | 6 | Avail | Avail | Avail | Ferdinand Schrey's German system, later folded into the unified script. Clean sweep, hard to spell from hearing. |
| **`stenos`** | 6 | Avail | Avail | Avail | Greek *stenós*, "narrow" — the root of *stenography*. Clean sweep, but reads as a plural and its search space is full of spinal stenosis. |
| **`calamo`** | 6 | Avail | Avail | Avail | From *currente calamo*, "with a running pen" — written straight through without revision. Clean sweep; the idiom is too obscure to carry itself. |
| **`tironian`** | 8 | Avail | Avail | Avail | Tiro's system in adjective form. Clear on all three registries, near-virgin on GitHub, and twice as long as the brief allows. |
| **`scopist`** | 7 | Avail | Avail | Avail | The court-reporting job title for whoever edits a stenographer's raw output into a finished transcript. "Scope" reads as scoping to a programmer. |
| **`duployan`** | 8 | Avail | Avail | Avail | The Duployé system, adapted for languages from French to Chinook. Only footprint is a Unicode font; hard to spell from hearing. |
| **`stenomask`** | 9 | Avail | Avail | Avail | The hood a court reporter speaks into to re-voice testimony inaudibly. Vivid, unclaimed, long. |
| **`amanuensis`** | 10 | Avail | Avail | Avail | Literally "one who writes from dictation" — the most accurate word in English for this program, and far too long to type ten times a day. |
| **`brevigraph`** | 10 | Avail | Avail | Avail | A manuscript abbreviation mark. Same problem. |
| — | | | | | |
| **`ductus`** | 6 | Avail | Taken | Taken | In palaeography, the path and order of the strokes that form a letter. |
| **`tittle`** | 6 | Avail | Taken | Taken | The dot over an `i`, as in "jot and tittle" — the smallest written mark that changes meaning. |
| **`uncial`** | 6 | Avail | Taken | Avail | The majuscule script of late-antique manuscripts. Handsome; about letterforms, not speech. |
| **`ectype`** | 6 | Avail | Taken | Avail | A copy taken from an original, as opposed to the prototype. |
| **`stapes`** | 6 | Avail | Taken | Avail | The stirrup bone — the last link in the chain that carries sound into the inner ear. The transducer, anatomically. |
| **`rhema`** | 5 | Avail | Taken | Avail | Greek *rhēma*, "the thing that was said". |
| **`brevis`** | 6 | Avail | Taken | Taken | Latin "short", the root under *brachygraphy* and *brevigraph*. |
| **`akouo`** | 5 | Avail | Avail | Avail | Greek "I hear". Clean sweep, but a 10★ audio "listening contract" library already sits on it. |

## Rejected, and why

Recorded so the search is not repeated.

| Name | Reason |
|---|---|
| **`tiro`** | **PyPI taken.** Cicero's secretary, who invented shorthand to keep up with a live speaker — the best story and the best command name found in this search, cut by the registry rule. `notae` and `tironian` carry the same reference; `byrom` carries the same shape. |
| **`boswell`**, **`hansard`**, **`calamus`**, **`notarius`**, **`dictum`** | **PyPI taken.** All previously shortlisted. Boswell is idiomatic English for a verbatim recorder of another's words; Hansard is the proper noun for a transcript of what was spoken; a *notarius* was a Roman shorthand clerk. |
| **`voce`** | PyPI-clear at four characters, and then `Privoce/vocechat-web` turns out to be a 2.3k★ chat application. Wrong domain to share a name with. |
| **`fari`** | Latin "to speak", the root under *infant* and *fate*. 16,800 GitHub repos and a 318★ VTT app on the exact name. |
| **`loqui`** | Latin "to speak". **Collides with an existing distro command.** |
| **`kurz`**, **`boyd`**, **`sloan`** | PyPI-clear, but each is a common surname with a 1,000+ repo namespace. `kurz` also carries *Kurzschrift*, German for shorthand, and an Austrian chancellor. |
| **`tympan`** | **Domain collision.** Tympan is an active open-source hearing-aid hardware platform with 150★ and its own GitHub org. A shame: the word means both the eardrum and the packing behind a press platen. |
| **`earshot`** | **Domain collision.** `pykeio/earshot` is a ~200★ streaming voice-activity-detection library. This project contains a VAD. |
| **`palantype`** | Palantype is a real chorded-shorthand system and `opensteno/palantype` belongs to the Open Steno Project — the people behind Plover. |
| **`plover`** | The open-source stenography engine. Taken by the obvious incumbent. |
| **`respeaker`** | "Respeaking" is the industry term for producing live subtitles by re-voicing into speech recognition. Perfect meaning; ReSpeaker is Seeed Studio's mic-array line. |
| **`murmur`**, **`parley`** | **Existing commands.** The Mumble VoIP server and a KDE vocabulary trainer. |
| **`myna`**, **`quipu`**, **`rebus`**, **`stet`**, **`jot`**, **`glyph`**, **`telex`**, **`breve`**, **`dicto`**, **`scribo`**, **`lexis`**, **`epos`**, **`stylo`**, **`scrive`** | PyPI taken. The off-brand vein: the bird that says your words back, the Inca knotted-cord record, the picture that stands for a sound, the proofreader's "let it stand". |
| **`griot`**, **`wampum`** | The West African oral historian and the belts that recorded spoken treaties. Evocative, and not ours to take for a dictation utility. |
| **`steno`**, **`scribe`**, **`quill`**, **`verbatim`**, **`dictate`**, **`utter`**, **`whispering`** | Generic or exhausted — taken on every registry, thousands of GitHub hits, no distinguishing story. |
| Anything with `sway` or `way` | Out of scope by decision. Removes the original shortlist entirely: `swayvox`, `wayscribe`, `swayscribe`, `voxway`, `sayway`, `waytalk`, `swaytalk`, `waystt`, `swaystt`, `waydictate`, `whisperway`, and the rest. |

## Outcome

**`mavor`**, chosen from the finalists above on 2026-09-03.

The runners-up, in the order they were ranked, in case the decision is ever
reopened:

1. **`byrom`** — John Byrom invented a shorthand system in the 1720s and sold it
   as a subscription secret; the Wesleys learned it, and his journals are
   written in it. The strongest story in the search, and the nearest thing to
   the name the PyPI rule cut. Lost on sound: the double consonant is a beat
   slower to say and to type than `mavor`.
2. **`siglum`** — the one scribal mark that stands in for a whole word. The
   choice if the name should describe the mechanism rather than name a person.
3. **`teeline`** — the shorthand British journalists learn. Two characters over
   budget, and the only candidate that already reads as a product to someone who
   does not know the reference.

## Decision Ledger

- **The name is `mavor`** (2026-09-03). Applied across the tree with no
  compatibility shim: the config, model cache, log and history live at their
  `mavor` paths and nowhere else.
- **PyPI availability is a hard requirement, not a preference.** Nothing is
  shipped to PyPI today, but a squatted name forecloses ever publishing a helper
  package under the project's own name. This cut `tiro`, `boswell`, `hansard`,
  `calamus`, and `notarius` from the shortlist.
- **Names built on `sway`, `way`, `vox`, `stt`, or `speech` are out of scope.**
  They name the stack, not the product, and nothing about the product is
  Wayland-specific from the user's side.
- **Target length is four to six characters.** `teeline`, `shelton`, and
  `tironian` remain listed but are over budget.

## Open Questions

- ~~**Does the binary name have to match the repo name?**~~ Settled by the
  outcome: binary, module path, repo and config directory are all `mavor`.
- **Is a dedicated GitHub org wanted?** Every one-word name is squatted as a
  username, so an org would need a suffixed or compound form. Current
  assumption: no — the project lives at `mschulkind-oss/<name>`.
