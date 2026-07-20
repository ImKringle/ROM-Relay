// Records a sha256 of every UI source file into dist/.manifest.
//
// dist/ is committed so that building ROM Relay never needs Node. The risk that
// buys is a stale dist: someone edits src/ and forgets to rebuild, and the binary
// serves yesterday's assets with no error. A Go test recomputes these hashes and
// fails if they disagree, which turns a silent staleness bug into a failed test.
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, writeFileSync, statSync } from "node:fs";
import { join, posix, relative, sep } from "node:path";

// Both directories feed the build: src/ is compiled directly, and Tailwind
// generates utilities by scanning the templates, so a template that introduces a
// new class changes the CSS just as surely as editing app.css does.
const uiRoot = join("internal", "web", "ui");
const sourceDirectories = [join(uiRoot, "src"), join(uiRoot, "templates")];
const manifestPath = join(uiRoot, "dist", ".manifest");

function filesUnder(directory) {
  return readdirSync(directory).flatMap((entry) => {
    const full = join(directory, entry);
    return statSync(full).isDirectory() ? filesUnder(full) : [full];
  });
}

const entries = sourceDirectories
  .flatMap((directory) => filesUnder(directory))
  .map((file) => {
    const hash = createHash("sha256").update(readFileSync(file)).digest("hex");
    // Slash-separated so the manifest is identical on Windows and Linux.
    const name = relative(uiRoot, file).split(sep).join(posix.sep);
    return `${hash}  ${name}`;
  })
  .sort();

writeFileSync(manifestPath, entries.join("\n") + "\n");
console.log(`ui-manifest: recorded ${entries.length} source file(s)`);
