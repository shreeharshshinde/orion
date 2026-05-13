import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatRelativeTime(value?: string) {
  if (!value) return "Not set";

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Unknown";

  const delta = Date.now() - date.getTime();
  const minutes = Math.round(delta / 60000);

  if (Math.abs(minutes) < 1) return "Just now";
  if (Math.abs(minutes) < 60) return `${Math.abs(minutes)}m ${delta >= 0 ? "ago" : "from now"}`;

  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return `${Math.abs(hours)}h ${delta >= 0 ? "ago" : "from now"}`;

  const days = Math.round(hours / 24);
  return `${Math.abs(days)}d ${delta >= 0 ? "ago" : "from now"}`;
}
