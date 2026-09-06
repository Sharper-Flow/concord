// Code stamped by scripts/install.py at install time; DO NOT EDIT.
// CD-0111 D1: a session runs every core call against the release it started
// on. The installer writes the absolute release paths into its copy of this
// file. This repository copy is the unstamped placeholder: an adapter that
// runs from a checkout has no release to bind to and refuses every core call
// with missing_binary rather than resolving `concord` through PATH.
export const releaseRoot: string = ""
export const coreBinary: string = ""
