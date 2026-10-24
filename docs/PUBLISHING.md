# Publishing Quarry

The current application uses Quarry branding, a generated launcher icon, Fluent icons with license notices, and screenshots rendered from fictional data. Its original source and generated artwork are MIT licensed. Keep all third-party notices with source and binary distributions. The packaged license copy is `resources/licenses/Quarry-LICENSE.txt`; keep it identical to the root `LICENSE`.

## Existing repository and rewritten history

Continue using the existing GitHub repository. The publication cleanup removes `internal/desktop/assets/brand.png` from Git history, including branches and release tags. Rewritten commits and tags have new object IDs. Collaborators should clone again or carefully rebase their work onto the cleaned history; merging an old branch can restore removed history.

A Git rewrite does not remove separately uploaded release binaries, release screenshots, pull-request references, forks or cached objects. Review older release downloads before making the repository public, particularly releases made before the Quarry rebrand. GitHub may retain old objects accessible by their original IDs even after a force push.

Build and test using Go 1.27.1 or later. `scripts/build.ps1 -Portable` includes the application license and dependency notices. `scripts/export-public.ps1` remains an optional way to export the current source without Git metadata; a new repository is not required.

## Scope and remaining ownership review

This cleanup is not legal clearance. Confirm that contributed code was independently written or used with permission and was not obtained through decompilation or extraction from proprietary software. Replacing branding or rewriting history does not cure any ownership issue in retained code. General disk-analysis features can be implemented independently; copying protected artwork or other expression requires appropriate rights. A non-affiliation statement does not grant those rights.

Existing author identities remain in commit history. Use your GitHub noreply email for future commits if desired. Do not commit real scan exports, settings, credentials, private keys or screenshots of personal files. Ignore rules reduce accidental additions but do not remove files already tracked or in history.

The publication review checked current tracked files, asset documentation and common credential patterns in history. It was not a comprehensive secret scan, dependency security audit or trademark clearance for the Quarry name.
