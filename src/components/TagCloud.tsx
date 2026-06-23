import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { fetchTags, type TagItem } from "@/data/videos";

export function TagCloud() {
  const [params] = useSearchParams();
  const activeTag = params.get("tag");
  const [tags, setTags] = useState<TagItem[]>([]);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let active = true;
    const timer = window.setTimeout(() => {
      fetchTags().then((list) => {
        if (active) setTags(list);
      });
    }, 500);
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, []);

  useEffect(() => {
    const slider = containerRef.current;
    if (!slider) return;

    let isDown = false;
    let startX = 0;
    let scrollLeft = 0;
    let isDragging = false;

    const handleMouseDown = (e: MouseEvent) => {
      isDown = true;
      isDragging = false;
      slider.classList.add("is-dragging");
      startX = e.pageX - slider.offsetLeft;
      scrollLeft = slider.scrollLeft;
    };

    const handleMouseLeave = () => {
      isDown = false;
      slider.classList.remove("is-dragging");
    };

    const handleMouseUp = () => {
      isDown = false;
      slider.classList.remove("is-dragging");
    };

    const handleMouseMove = (e: MouseEvent) => {
      if (!isDown) return;
      e.preventDefault();
      const x = e.pageX - slider.offsetLeft;
      const walk = (x - startX) * 1.5;
      if (Math.abs(x - startX) > 10) {
        isDragging = true;
      }
      slider.scrollLeft = scrollLeft - walk;
    };

    const handleWheel = (e: WheelEvent) => {
      if (e.deltaY !== 0) {
        e.preventDefault();
        slider.scrollLeft += e.deltaY;
      }
    };

    const handleClick = (e: MouseEvent) => {
      if (isDragging) {
        e.preventDefault();
        e.stopPropagation();
        isDragging = false;
      }
    };

    slider.addEventListener("mousedown", handleMouseDown);
    slider.addEventListener("mouseleave", handleMouseLeave);
    slider.addEventListener("mouseup", handleMouseUp);
    slider.addEventListener("mousemove", handleMouseMove);
    slider.addEventListener("wheel", handleWheel, { passive: false });
    slider.addEventListener("click", handleClick, { capture: true });

    return () => {
      slider.removeEventListener("mousedown", handleMouseDown);
      slider.removeEventListener("mouseleave", handleMouseLeave);
      slider.removeEventListener("mouseup", handleMouseUp);
      slider.removeEventListener("mousemove", handleMouseMove);
      slider.removeEventListener("wheel", handleWheel);
      slider.removeEventListener("click", handleClick, { capture: true });
    };
  }, [tags]);

  if (tags.length === 0) return null;

  // 将标签分为奇偶两行，使其横向自由流式排布，不发生强制的列对齐
  const row1 = tags.filter((_, idx) => idx % 2 === 0);
  const row2 = tags.filter((_, idx) => idx % 2 !== 0);

  const renderTag = (tag: TagItem) => (
    <Link
      key={tag.id}
      to={`/list?tag=${encodeURIComponent(tag.label)}`}
      className={`tag-chip ${activeTag === tag.label ? "is-active" : ""}`}
      title={
        typeof tag.count === "number" ? `${tag.count} 个视频` : undefined
      }
    >
      {tag.label}
      {typeof tag.count === "number" && tag.count > 0 && (
        <span style={{ marginLeft: 4, opacity: 0.7 }}>({tag.count})</span>
      )}
    </Link>
  );

  return (
    <div className="tag-cloud-container" aria-label="热门标签">
      <div className="tag-cloud__grid" ref={containerRef}>
        <div className="tag-cloud__row">
          {row1.map(renderTag)}
        </div>
        <div className="tag-cloud__row">
          {row2.map(renderTag)}
        </div>
      </div>
    </div>
  );
}
