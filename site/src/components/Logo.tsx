/** A prompt chevron whose underline is a link that breaks and rejoins. */
export function Logo({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" className="logo">
      <rect width="24" height="24" rx="6" className="logo__bg" />
      <path d="M6 7l4.5 4.5L6 16" className="logo__stroke" />
      <path d="M12.5 17h2.2M16.8 17h1.7" className="logo__stroke" />
    </svg>
  );
}
