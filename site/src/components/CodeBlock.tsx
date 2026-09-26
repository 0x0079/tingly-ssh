import { useState } from "react";

interface Props {
  code: string;
  label?: string;
  copyText: { copy: string; copied: string };
}

export function CodeBlock({ code, label, copyText }: Props) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard unavailable (insecure context); the text is still selectable
    }
  }

  return (
    <div className="code">
      <div className="code__bar">
        <span className="code__label">{label}</span>
        <button type="button" className="code__copy" onClick={copy}>
          {copied ? copyText.copied : copyText.copy}
        </button>
      </div>
      <pre>
        <code>{code}</code>
      </pre>
    </div>
  );
}
