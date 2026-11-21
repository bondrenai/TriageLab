import type { RunSummary } from "./types/investigation";

const IGNORED_DIRECTORY_SEGMENTS = new Set([
  ".git",
  ".venv",
  "venv",
  "node_modules",
  "__pycache__",
  ".pytest_cache",
  ".ruff_cache",
  "dist",
  "build",
]);

export interface UploadEntry {
  file: File;
  path: string;
}

export interface SelectedUploadCandidate {
  name: string;
  webkitRelativePath?: string;
  size: number;
}

export function isIgnoredFolderPath(relPath: string): boolean {
  const segments = relPath.split("/").filter(Boolean);
  // Check directory segments (all segments except the final filename).
  for (let i = 0; i < segments.length - 1; i++) {
    if (IGNORED_DIRECTORY_SEGMENTS.has(segments[i])) {
      return true;
    }
  }
  return false;
}

export function prepareFolderSelection<T extends SelectedUploadCandidate>(
  selected: readonly T[],
  stripSharedRoot: boolean,
): { accepted: { item: T; path: string }[]; skippedIgnored: number } {
  const rawPaths = selected.map(item => item.webkitRelativePath || item.name);
  let stripRoot = false;
  if (stripSharedRoot && rawPaths.length > 0) {
    const firstRoot = rawPaths[0].split("/")[0];
    stripRoot = Boolean(firstRoot) && rawPaths.every(p => p.includes("/") && p.split("/")[0] === firstRoot);
  }
  const accepted: { item: T; path: string }[] = [];
  let skippedIgnored = 0;
  for (let i = 0; i < selected.length; i++) {
    const path = stripRoot ? rawPaths[i].slice(rawPaths[i].indexOf("/") + 1) : rawPaths[i];
    if (!path) continue;
    if (isIgnoredFolderPath(path)) {
      skippedIgnored++;
      continue;
    }
    accepted.push({ item: selected[i], path });
  }
  return { accepted, skippedIgnored };
}

export type RunHistoryFilter = "all" | "reproduced" | "not_reproduced" | "inconclusive" | "attention";

export function filterRunHistory(runs: readonly RunSummary[], query: string, filter: RunHistoryFilter): RunSummary[] {
  const needle = query.trim().toLowerCase();
  return runs.filter(run => {
    switch (filter) {
      case "reproduced":
        if (run.outcome !== "reproduced") return false;
        break;
      case "not_reproduced":
        if (run.outcome !== "not_reproduced") return false;
        break;
      case "inconclusive":
        if (run.outcome !== "inconclusive") return false;
        break;
      case "attention":
        if (!run.attention) return false;
        break;
    }
    if (!needle) return true;
    return (
      run.description.toLowerCase().includes(needle) ||
      run.id.toLowerCase().includes(needle) ||
      run.status.toLowerCase().includes(needle) ||
      run.outcome.toLowerCase().includes(needle)
    );
  });
}
