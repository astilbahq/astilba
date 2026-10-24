# Agentic development research

Reviewed on 22 October 2026, with a repository-structure follow-up on 24 October 2026, for Astilba's BYOS platform. This is a record of evidence and adoption decisions, not an additional instruction file. [Engineering principles](engineering-principles.md) contains the working policy; the approved product, architecture and implementation plans retain their authority.

The sources below are first-party publications whose article bodies were inspected. Dates are publication dates shown by the publisher. Engineering case studies, experiments, interviews and product guidance have different evidential weight. None establishes that agent-only engineering or release decisions are generally reliable.

## What we are adopting

These are Astilba's adaptations of the evidence, not a claim that the publishers validated this exact workflow.

| Practice | Astilba application |
| --- | --- |
| Bounded work with explicit intent | Use the existing issue and approved plans to establish the outcome, constraints and observable acceptance evidence. Keep one implementation issue active. Small changes do not need a formal specification. |
| Current, relevant context | Inspect the actual checkout and nearby code. Keep mandatory rules discoverable, load scoped instructions explicitly, and retrieve detailed documentation when the task needs it. |
| Deterministic enforcement | Preserve existing formatting, type, lint, packaging and security gates. Use ordinary scripts for mechanical steps instead of relying on an agent to remember them. |
| Evidence at the real boundary | Unit-test load-bearing decisions; use integration or browser checks when the guarantee depends on actual state, infrastructure or interaction. Challenge test expectations as well as implementation. |
| Independent, proportionate review | Keep the required `pd-iterative-peer-review` workflow before every PR. Its depth follows risk. Review against intent and contracts, and verify candidates before changing code. Agent independence does not eliminate shared blind spots. |
| Durable handoffs and bounded authority | Record material decisions in the design documents and progress/evidence in Linear. Use isolated worktrees for concurrent editing, disposable test resources and the access needed for the task. Preserve existing authorization boundaries. |
| Improve from observed failures | Inspect traces and recurring corrections, then adjust a relevant instruction, script or check. Reassess model-specific scaffolding using real tasks, without silently waiving required gates. |

The test policy remains unchanged: no default test-first process, no new coverage percentage targets, and no unit tests that merely restate implementation. The maintainer still owns consequential product, architecture and trust decisions. Routine work already authorized should proceed without repeated permission requests.

## Repository structure follow-up — 24 October 2026

**[OpenAI: Harness engineering](https://openai.com/index/harness-engineering/), 11 February 2026.** This engineering case study uses a short instruction entry point linked to deeper repository documentation, runnable environments per worktree, and mechanically enforced module boundaries. These are useful design examples, not a controlled comparison of folder layouts. Astilba already has scoped instructions and separate worktrees; the practical next step is to enforce an observed dependency problem rather than introduce a new architecture framework. Its release practices do not override our verification gates.

**[Evaluating AGENTS.md: Are Repository-Level Context Files Helpful for Coding Agents?](https://arxiv.org/html/2602.11988v3), revision 3, 29 September 2026.** This benchmark found no general task-success improvement from context files and over 20% higher average inference cost. Instructions were followed, but repository overviews did not provide the expected navigation benefit. The study concerns Python task resolution, not every language, security property or modern model. Together with Vercel's narrower positive result, it argues for testing guidance on real work rather than assuming more documentation helps.

Our structure decisions are adaptations of this evidence and the existing engineering principles:

| Decision | Application |
| --- | --- |
| Keep instructions small and specific | Existing scoped `AGENTS.md` files point to task-relevant references; do not generate a second tree of agent-only documentation. |
| Group code by responsibility | Colocate feature components, styling and private helpers. Split when responsibilities or consumers diverge, not at an arbitrary line count. |
| Make dependency direction explicit | Routing composes features. Shared API/query state lives below both. The dashboard's Vite+ (Oxlint) check rejects runtime import cycles. |
| Keep setup reproducible | Use pinned tools, normal package scripts and generated artifacts. Panda code generation runs during installation and before type checking. |
| Verify observable behaviour | Use the existing real-API browser journeys and local preview. Record actual failures and rework before adding more rules or tooling. |

The [frontend development reference](frontend-development.md) records the resulting boundaries and commands. This follow-up does not call for a wholesale file move, new agent framework, additional planning system or weaker review requirements.

## Vercel

**[AGENTS.md outperforms skills in our agent evals](https://vercel.com/blog/agents-md-outperforms-skills-in-our-agent-evals), 27 January 2026.** In Vercel's Next.js 16 evaluation, a compressed documentation index in `AGENTS.md` outperformed the tested skill approaches. The team also corrected ambiguous tasks, test leakage and implementation-specific assertions. This supports discoverability and behavioural checks. Its reported perfect score belongs to that suite; it is not proof that instructions always beat skills or that a newer model needs the same setup.

**[How to evaluate AI coding agents](https://vercel.com/i/evaluating-ai-coding-agents), 6 August 2026.** The guide emphasizes the whole working setup: repository context, existing-code reuse, verification and review capacity. It supports assessing completed work and rework rather than output volume. It combines advice, third-party research and product promotion; its cited statistics are not all Vercel experiments.

**[Making every harness durable](https://vercel.com/blog/making-every-harness-durable), 19 October 2026.** The design distinguishes preserving intent/results, recovering a process and deciding which tool calls can safely repeat. We adopt the practical retry distinction: inspect state before repeating a possible side effect. This is architecture guidance, not evidence that Astilba needs to build an agent runtime. Our platform's durable deployment operations remain governed by the existing architecture.

## Stripe

**[Minions: Stripe's one-shot, end-to-end coding agents — Part 2](https://stripe.dev/blog/minions-stripes-one-shot-end-to-end-coding-agents-part-2), 19 February 2026.** Stripe combines flexible agent steps with deterministic operations, scoped rules, isolated environments and bounded CI retries. Its reported throughput explicitly includes human review. We adopt fast local feedback, isolation and evidence; we do not infer that autonomous release is validated or copy Stripe's particular retry count.

**[Can AI agents build real Stripe integrations? We built a benchmark to find out](https://stripe.com/blog/can-ai-agents-build-real-stripe-integrations), 2 March 2026.** Eleven integration environments used checks of APIs, browser flows and resulting Stripe objects. Some agents mistook error responses for successful verification. This is a concrete reason to inspect resulting state and exercise valid success paths. It is a narrow benchmark using the models available at the time, not a general reliability estimate.

**[You can't whisper at an AI agent](https://stripe.dev/blog/ai-steering-experiments), 14 May 2026.** Steering experiments found that explicitly loaded guidance and blocking errors worked better than passive hints; modular skills helped in that evaluation. Guidance hidden inside dependencies was often missed. This does not contradict Vercel's result: whether relevant guidance enters the working context matters more than its filename alone.

**[AI didn't write our SDK. It changed how we built it.](https://stripe.dev/blog/ai-didnt-write-our-sdk-it-changed-how-we-built-it), 28 August 2026.** Bounded delegation, reviewed specifications, prototypes and persistent architectural guidance helped SDK work. Plausible output still violated deeper invariants. We adopt clear contracts and use our P0 experiments to test architectural assumptions; a convincing generated implementation cannot settle those assumptions by itself.

**[Stripe's Payment Method Factory: Orchestrating agents for repeated, custom integrations](https://stripe.dev/blog/stripes-payment-method-factory-orchestrating-agents-for-repeated-custom-integrations), 30 September 2026.** Repeated integration work benefited from reviewable PRs, prompts refined from observed failures, deterministic orchestration and sandbox contract checks. Its factory had many prior implementations to learn from. Reported delivery gains are observational and do not justify a similar platform for a solo project.

## Atlassian

**[How AI is Changing Developer Workflows: Lessons From Engineers](https://www.atlassian.com/blog/ai-at-work/how-ai-is-changing-developer-workflows), 27 May 2026.** Interviews describe context assembly, smaller tasks, clearer acceptance criteria and a need for visibility into agent actions. The article explicitly calls its small qualitative sample directional. It supports a compact task contract, not mandatory paperwork for every edit.

**[The Agentic Pivot: Why the work around code matters more than ever](https://www.atlassian.com/blog/company-news/the-agentic-pivot), 3 September 2026.** This commentary accompanies a survey of over 1,100 engineering respondents and emphasizes intent, constraints, decision history and durable records of agent work. Survey perceptions do not establish causal productivity gains. We use existing documents, issues and PR evidence for handoffs; the full report methodology was not inspected.

**[The AI SDLC transformation playbook](https://www.atlassian.com/blog/ai-at-work/ai-sdlc-transformation-playbook), 1 October 2026.** Its useful recommendations are accessible standards, review against requirements, visible work and improving one workflow using evidence and feedback. It also describes Atlassian's commercial products and internal approach. A solo project does not need its governance structure or collaboration stack.

## Anthropic

**[Harness design for long-running apps](https://www.anthropic.com/research/harness-design-long-running-apps), 24 March 2026.** App-building experiments separate generation from evaluation and exercise the running product. Evaluators still missed defects and needed calibration. Later experiments removed scaffolding as model capabilities changed, while some review remained useful. This supports inspecting real outcomes and testing whether workflow complexity earns its cost. These are particular experiments, not a universal prescription for planner/evaluator agents or fixed sprint structures.

**[Effective harnesses for long-running agents](https://www.anthropic.com/research/effective-harnesses-for-long-running-agents), 26 November 2025 — older background.** Incremental work, durable progress records and end-to-end checks address context loss and premature completion claims. We adopt resumable handoffs through existing artifacts. We do not copy its large feature checklist or extra progress files into a repository that already has plans and Linear.

## OpenAI

**[Custom Code Review rules for Codex](https://developers.openai.com/blog/custom-code-review-rules-for-codex), 20 July 2026.** Concise, scoped rules expose compatibility constraints and data boundaries a reviewer may not infer from the diff. Deterministic checks remain complementary. We retain project-specific invariants and the pre-PR review requirement near the code, without turning every preference into a mandatory review finding.

**[Iterating Development Workflows with Codex](https://developers.openai.com/cookbook/examples/codex/iterating-development-workflows-with-codex), 3 August 2026.** The recipe organizes goals, scope, acceptance evidence and durable context. It explicitly identifies most of its files as optional conventions. We use the documents already agreed for Astilba; its example approval gates and test-first sequences do not override our authorization or test policy.

**[Rethinking skills and prompts for GPT-6 Astra](https://developers.openai.com/blog/rethinking-skills-and-prompts-for-gpt-6-astra), 11 September 2026.** The guidance recommends concise skill descriptions, progressive disclosure, task-relevant document loading and revisiting accumulated instructions. Some recommendations concern that particular model. We adopt instruction maintenance, not the assumption that a stronger model removes our verification or review requirements.

## Boundaries on adoption

Do not introduce an enterprise agent platform, mandatory multi-agent implementation, a bash-only tool policy, a separate memory database, transcript archives or a second set of planning files on the strength of these articles. Tools and reusable prompts should solve observed recurring work. Benchmarks and self-reported PR counts do not set Astilba's quality targets.

The immediate workflow remains: take one scoped P0 issue, inspect the relevant context, implement against observable acceptance criteria, gather runtime evidence, run the required checks and independent review, then update the issue and plans. There is no new framework to build before starting the product.
