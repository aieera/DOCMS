import { useRef } from "react";
import type { Folder } from "../lib/types";

// FolderTree renders the customer's subfolders as an accessible tree
// (role="tree" / "treeitem", arrow-key navigation, roving focus). The layout is
// flat (main → 6 subfolders), so Up/Down move between items and Enter/Space
// selects.
export function FolderTree({
  folders,
  selectedId,
  onSelect,
}: {
  folders: Folder[];
  selectedId?: string;
  onSelect: (id: string) => void;
}) {
  const refs = useRef<(HTMLDivElement | null)[]>([]);

  function onKeyDown(e: React.KeyboardEvent, i: number) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      refs.current[Math.min(i + 1, folders.length - 1)]?.focus();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      refs.current[Math.max(i - 1, 0)]?.focus();
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onSelect(folders[i].id);
    }
  }

  return (
    <ul role="tree" aria-label="Customer folders" className="space-y-0.5">
      {folders.map((f, i) => {
        const selected = f.id === selectedId;
        return (
          <li key={f.id} role="none">
            <div
              role="treeitem"
              aria-selected={selected}
              tabIndex={i === 0 ? 0 : -1}
              ref={(el) => (refs.current[i] = el)}
              onKeyDown={(e) => onKeyDown(e, i)}
              onClick={() => onSelect(f.id)}
              className={`flex cursor-pointer items-center justify-between rounded px-2 py-1.5 text-sm ${
                selected ? "bg-brand/10 font-medium text-brand" : "hover:bg-gray-100"
              }`}
            >
              <span className="flex items-center gap-2">
                <FolderIcon /> {f.name}
              </span>
              <span className="text-xs text-gray-400">{f.documentCount}</span>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

function FolderIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden className="shrink-0 text-gray-400">
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  );
}
