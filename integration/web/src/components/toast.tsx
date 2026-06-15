import { useEffect, useState } from "react";

// A tiny dependency-free toaster: a module-level store + a <Toaster/> mounted
// once in App. Used for transient notices like a 409 "already resolved" on a
// concurrent action — info, not a failure.
export type ToastKind = "info" | "success" | "error";
interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

let counter = 0;
let current: Toast[] = [];
const listeners = new Set<(t: Toast[]) => void>();
function emit() {
  for (const l of listeners) l(current);
}

export function toast(message: string, kind: ToastKind = "info", ttlMs = 4000) {
  const id = ++counter;
  current = [...current, { id, kind, message }];
  emit();
  setTimeout(() => {
    current = current.filter((t) => t.id !== id);
    emit();
  }, ttlMs);
}

const kindStyles: Record<ToastKind, string> = {
  info: "border-blue-200 bg-blue-50 text-blue-800",
  success: "border-green-200 bg-green-50 text-green-800",
  error: "border-red-200 bg-red-50 text-red-800",
};

export function Toaster() {
  const [items, setItems] = useState<Toast[]>(current);
  useEffect(() => {
    listeners.add(setItems);
    return () => {
      listeners.delete(setItems);
    };
  }, []);
  if (items.length === 0) return null;
  return (
    <div className="fixed bottom-4 right-4 z-50 flex max-w-sm flex-col gap-2" aria-live="polite">
      {items.map((t) => (
        <div
          key={t.id}
          role="status"
          className={`rounded border px-3 py-2 text-sm shadow ${kindStyles[t.kind]}`}
        >
          {t.message}
        </div>
      ))}
    </div>
  );
}
