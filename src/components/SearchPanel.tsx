import { FormEvent, useCallback, useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Search } from "lucide-react";

const SEARCH_DEBOUNCE_MS = 500;

type SearchPanelProps = {
  value?: string;
  onSearch?: (keyword: string) => void;
};

export function SearchPanel({ value, onSearch }: SearchPanelProps = {}) {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const urlKeyword = params.get("q") ?? "";
  const committedKeyword = value ?? urlKeyword;
  const [keyword, setKeyword] = useState(committedKeyword);

  const commitSearch = useCallback((value: string) => {
    const q = value.trim();
    if (onSearch) {
      onSearch(q);
      return;
    }
    const sp = new URLSearchParams();
    if (q) sp.set("q", q);
    const query = sp.toString();
    navigate(query ? `/list?${query}` : "/list");
  }, [navigate, onSearch]);

  useEffect(() => {
    setKeyword(committedKeyword);
  }, [committedKeyword]);

  useEffect(() => {
    if (keyword.trim() === committedKeyword.trim()) return;
    const timer = window.setTimeout(() => {
      commitSearch(keyword);
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [commitSearch, committedKeyword, keyword]);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    commitSearch(keyword);
  }

  return (
    <form className="search-panel" onSubmit={handleSubmit} role="search">
      <div className="search-panel__form">
        <div className="search-panel__input-wrapper">
          <Search size={16} className="search-panel__search-icon" />
          <input
            className="search-panel__input"
            type="text"
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            placeholder="搜索视频标题或作者"
            aria-label="搜索关键词"
          />
        </div>
        <button className="search-panel__submit" type="submit">
          <Search size={16} className="search-panel__submit-icon" />
          <span className="search-panel__submit-text">搜索</span>
        </button>
      </div>
    </form>
  );
}
