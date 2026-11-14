# Incident response

What an operator does when something has gone wrong with an installation's security. Each
section is one situation, with what to check first, the commands to run and what the people
affected do next. Run the commands on the management host with the same configuration as the
serving process, as for the other [server commands](hosting.md).

## Account takeover or lost authenticator

Use this when an account holder cannot sign in because two-factor authentication stands in
their way:

- **Lost authenticator.** The authenticator is gone and no recovery codes are left.
- **Foreign authenticator.** Someone who knew the password turned on two-factor authentication
  with an authenticator of their own. A password reset ends that person's sessions but leaves the
  authenticator in place, so the holder still cannot sign in. The holder was emailed when it
  was turned on.

Nothing in the product removes an authenticator without a current code from it, so this needs
the operator.

### Before you run it

The reset hands the account to whoever controls its email address, because the holder gets back
in through an emailed password reset. Before running it, confirm that the person asking still
controls that mailbox, and that the mailbox itself was not part of the takeover. If it was, help
them recover the mailbox first.

The installation needs working email for the same reason. Without SMTP configured, the command
refuses, because the holder could not reset their password afterwards. Configure SMTP first, as
described in [hosting](hosting.md).

### Reset two-factor authentication

```sh
astilba reset-mfa --email ADDRESS --reason "REASON"
```

The reason is one line of at most 500 bytes, which is 500 characters of plain English. The
holder reads it in their notice, so write it for them, for example `You asked on 14 November after losing your phone`. It is kept with the
account's security events, as the record of why the operator acted.

In one transaction the command:

- turns two-factor authentication off and deletes the recovery codes;
- replaces the password with a random one that nobody knows, and ends any password reset link
  sent before;
- ends every session, terminal and automation token of the account, and withdraws its approval of
  automatic deploys and previews, as any password change does;
- records an `mfa_reset` account security event with the reason, and queues the holder's
  notice.

The password goes too because the operator cannot tell a holder who lost their phone from
someone who phished the password and asked, and because whoever enrolled a foreign
authenticator knows the password. Signing in with the old password fails as any wrong password
does.

The serving process sends the notice, so keep it running. It retries a failed delivery for up to
24 hours. The command logs the account and event IDs, but not the reason or the address.

The command refuses an address with no account, and an account without two-factor
authentication; that holder can reset their own password.

### What the holder does next

1. Choose a new password with **Forgot password** on the sign-in page, or at `/reset`. If the
   mail does not arrive, see [when password reset mail fails](hosting.md#when-password-reset-mail-fails).
2. Sign in, set up two-factor authentication again and save the new recovery codes. Until then,
   a workspace that requires two-factor authentication, and every workspace on a hosted
   installation, refuses the account's changes.
3. Approve again any automatic deploys and previews that the account had approved, and issue
   new automation tokens.

After a foreign authenticator, the holder and their workspace owners should also review each
workspace's audit history for what was done while the intruder held the account, and rotate any
secret the account could read.
