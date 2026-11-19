import { Link } from "react-router";
import { Icon, icons } from "./ui";

export function BackLink({ to, label }: { to: string; label: string }) {
  return (
    <Link to={to} className="mb-4 inline-flex items-center gap-1.5 text-sm no-underline">
      <Icon d={icons.back} /> {label}
    </Link>
  );
}
