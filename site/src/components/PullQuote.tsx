/** A large statement set apart from the running text. */
export function PullQuote({ text, cite }: { text: string; cite: string }) {
  return (
    <figure className="wide quote">
      <blockquote>
        <p>{text}</p>
      </blockquote>
      <figcaption>{cite}</figcaption>
    </figure>
  );
}
