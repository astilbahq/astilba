---
version: 1
slug: "apps-platform-src-router-tsx"
primary_target: "apps/platform/src/router.tsx"
related_targets:
  [
    "apps/platform/src/features/auth/auth-frame.tsx",
    "apps/platform/src/features/auth/auth.styles.ts",
    "apps/platform/src/features/auth/password-reset.tsx",
    "apps/platform/src/features/auth/invitation.tsx",
    "apps/platform/src/features/auth/mfa-challenge.tsx",
  ]
---

# Authentication

Visitor mode: Operate. Daily sign-in for developers and small teams, on hosted and self-hosted installations. Preserve session gates, return destinations, MFA, reset, invitations and conditional hosted signup. The owner selected Aligned stack after refining the Conventional direction; typography is provisional and excludes Apfel.

## Direction contract

THESIS: Familiar, focused account access. Shared alignment and measured grouping provide cohesion without a surrounding card.

OWN-WORLD: An open #fdfdfd surface with #0d0d0d text, #6c6c6c muted text, a neutral near-black primary action, light uniform neutral-grey input borders, restrained corners and system sans-serif. Dark mode uses #121212, white text and 70% #cacaca muted text with the same hierarchy. No dark input underline or outer container border.

STORY: Recognise Astilba, enter credentials, recover access when needed, and continue to the originally requested page.

FIRST VIEWPORT: The logo is temporarily omitted at the owner's request. A centred 360px-wide column with a shared left edge, 24px heading, 24px group spacing and 20px form spacing. Password recovery sits beside the Password label; the hosted-beta action appears below the primary button when available, aligned left. Invitations are entered through their email links. Sign-in fields have visible labels and readable placeholders, with light full outlines. On narrow screens the column contracts within 28px page gutters and long forms scroll naturally. Focus is visible, submission has persistent feedback, and failed submissions retain entered data.

FORM: User-selected Aligned stack, superseding the Conventional card and earlier live variants. Code-led refinement of seed `seed:c778080728021839`. The owner said to ignore experimental slider tuning; use the variant defaults. The signature is consistent alignment, grouping and precise focus across auth states.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

PASSWORD RECOVERY: Email-only request, check-email confirmation, and a new-password form reached through an emailed `/reset?token=…` link. No code-entry field or direct code-entry action. Use the sign-in field recipe and left-aligned links. Preserve drafts on failure and announce stage changes through heading focus.

WORKSPACE INVITATIONS: Invitation emails link to `/invitation?token=…`; no manual code field. Missing-token visits explain where to find the link. Use shared fields for new accounts, explicit acceptance for signed-in users, and start-aligned secondary links. Preserve the invitation through sign-in, then focus the success heading and offer project access.

APPEARANCE: A 40px ghost icon button sits at the top inline-end, 16px from the edges on narrow screens and 24px on desktop. The Base UI tooltip names it Appearance; its radio menu offers Light, Dark and System with a selected checkmark. Selecting a theme leaves the menu open. System follows OS changes live, and explicit choices persist. Popups keep the auth palette through the portal.

MENU MOTION: The shared dropdown uses a 250ms scale-and-fade entrance (0.97 → 1) and 150ms exit (1 → 0.99), anchored to the trigger through Base UI. Timing is tokenised; reduced-motion mode is instant and closing popups ignore pointer input.

MENU FINISH: 12px popup corners, 6px padding, 13px text; 32px minimum rows with 8px corners and icon gaps, and 16px icons. Neutral hover fill is #f4f4f5 in light mode and 4% white in dark mode. Item hover fills transition over 200ms with the shared button timing; focus outlines remain immediate. Theme-specific layered shadows replace the border; forced colours retain a visible system border. Theme changes use one 250ms snapshot crossfade, with component transitions temporarily disabled and then restored. Initial load, reduced motion and unsupported browsers switch immediately.

TWO-FACTOR SIGN-IN: An authenticator-code form is the default after password verification. The owner selected six empty Base UI OTP slots using the shared input recipe, without placeholder dashes, with tokenised spacing and an explicit Verify button. Full-code paste, one-time-code autocomplete and a numeric keyboard remain available. A separate recovery-code mode keeps one text input with the same shared border, reached through “Use recovery code” below Verify. A quieter “Use another account” action follows it. Keyboard order matches this visible sequence. Both modes have specific instructions and validation, and a keyboard-accessible way back. Slots stay LTR, including arrow-key behavior, inside RTL pages. Switching stages clears the prior stage’s input and errors and focuses the heading; language changes preserve the current draft and focus. Use another account ends the pending session, preserving the safe return destination. Password reset remains on the initial sign-in screen.

FIELD BORDERS: Input, password, OTP, select and textarea use one appearance. Quiet is removed. Idle borders are #939393 on #fdfdfd (3.020:1) and #616161 on #121212 (3.025:1). Hover/focus keeps #8c8c8c/#707070; focus remains a flush 2px visual edge.
