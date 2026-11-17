# Leaving a node of this project

The Bitcoin and Lightning node of this project is not maintained. v0.7.0 was
not released, so there is no newer node to move to. This page is for a node
installed from an earlier release (the rlvpn program) or from a v0.7.0
candidate.

Empty a node that holds funds while it still works.

1. Keep the node healthy and online while you plan.
2. Close the Lightning channels cooperatively.
3. Wait for every close to confirm and for the funds to become spendable.
4. Move the funds to a wallet you control and check the result there.
5. Keep the seed and the recovery material safe.

Do not use a static channel backup as the normal way to leave a healthy
node. Recovery from it is an emergency procedure that force closes the
channels. Never run two LND instances from the same seed.

If the node cannot be run safely enough to close its channels, stop and
follow LND's disaster recovery guidance for the exact failure.

Do not run a newer installer of this project over the node.
