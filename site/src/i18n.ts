import { useEffect, useState } from "react";
import type { Content, Lang } from "./content/types";
import { en } from "./content/en";
import { zh } from "./content/zh";

const CONTENT: Record<Lang, Content> = { en, zh };
const KEY = "tingly-lang";

function initialLang(): Lang {
  const q = new URLSearchParams(window.location.search).get("lang");
  if (q === "en" || q === "zh") return q;
  try {
    const saved = localStorage.getItem(KEY);
    if (saved === "en" || saved === "zh") return saved;
  } catch {
    // storage may be blocked; fall through to the browser language
  }
  return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

export function useLang() {
  const [lang, setLang] = useState<Lang>(initialLang);
  const t = CONTENT[lang];

  useEffect(() => {
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
    document.title = t.meta.title;
    document.querySelector('meta[name="description"]')?.setAttribute("content", t.meta.description);
    try {
      localStorage.setItem(KEY, lang);
    } catch {
      // not persisting is fine
    }
  }, [lang, t]);

  return { lang, setLang, t };
}
