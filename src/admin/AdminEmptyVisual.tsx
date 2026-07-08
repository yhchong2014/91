import emptyImage from "@/assets/admin/empty.webp";
import noResultsImage from "@/assets/admin/no-results.webp";

type AdminEmptyVisualProps = {
  variant: "empty" | "no-results";
  text: string;
  className?: string;
};

export function AdminEmptyVisual({ variant, text, className = "" }: AdminEmptyVisualProps) {
  const image = variant === "no-results" ? noResultsImage : emptyImage;

  return (
    <div className={`admin-empty-visual${className ? ` ${className}` : ""}`}>
      <span className="admin-empty-visual__media" aria-hidden="true">
        <img className="admin-empty-visual__image" src={image} alt="" />
      </span>
      <span className="admin-empty-visual__text">{text}</span>
    </div>
  );
}
