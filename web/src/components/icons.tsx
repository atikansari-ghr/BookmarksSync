// Decorative inline icons (no external images: the page runs under a strict CSP).

export function BookmarkLogo({ size = 32 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M6 3h12a1 1 0 0 1 1 1v17l-7-4.5L5 21V4a1 1 0 0 1 1-1z" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinejoin="round" />
    </svg>
  )
}

export function PasskeyIcon() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <g fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
        <path d="M12 3a8 8 0 0 0-8 8v2" />
        <path d="M20 13v-2a8 8 0 0 0-4-6.9" />
        <path d="M8 21a10 10 0 0 0 1-4v-6a3 3 0 0 1 6 0v3" />
        <path d="M12 11v6a14 14 0 0 1-1.5 5" />
        <path d="M15 17a15 15 0 0 1-1 4" />
        <path d="M18 16v1" />
      </g>
    </svg>
  )
}

export function EyeIcon({ open }: { open: boolean }) {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <g fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
        <path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z" />
        <circle cx="12" cy="12" r="3" />
        {!open && <path d="M4 4l16 16" />}
      </g>
    </svg>
  )
}

export function ArrowIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M5 12h13M13 6l6 6-6 6" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

/** A neutral provider badge (the provider name is the label; no brand artwork). */
export function ProviderBadge({ name }: { name: string }) {
  return (
    <span className={`provider-badge provider-${name.toLowerCase()}`} aria-hidden="true">
      {name.charAt(0).toUpperCase()}
    </span>
  )
}

/** The hero illustration: glassy bookmark cards linked over teal water. */
export function HeroArt() {
  return (
    <svg className="hero-art" viewBox="0 0 640 520" aria-hidden="true" focusable="false" preserveAspectRatio="xMidYMax slice">
      <defs>
        <linearGradient id="wave1" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#7dd3fc" stopOpacity="0.55" />
          <stop offset="1" stopColor="#0e7490" stopOpacity="0.9" />
        </linearGradient>
        <linearGradient id="wave2" x1="0" y1="0" x2="1" y2="0">
          <stop offset="0" stopColor="#2dd4bf" stopOpacity="0.55" />
          <stop offset="1" stopColor="#0891b2" stopOpacity="0.8" />
        </linearGradient>
        <linearGradient id="card" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#ffffff" stopOpacity="0.62" />
          <stop offset="1" stopColor="#e0f7fa" stopOpacity="0.34" />
        </linearGradient>
      </defs>
      <path d="M0 330 C140 270 260 380 400 320 S600 230 640 250 V520 H0Z" fill="url(#wave1)" />
      <path d="M0 400 C160 350 300 450 460 380 S610 330 640 345 V520 H0Z" fill="url(#wave2)" />
      <path d="M0 460 C180 430 330 500 500 450 S620 425 640 430 V520 H0Z" fill="#0e7490" fillOpacity="0.55" />
      <g fill="none" stroke="#ccfbf1" strokeWidth="2.5" strokeOpacity="0.9">
        <path d="M180 260 Q190 170 245 165" />
        <path d="M395 165 Q470 175 470 225" />
        <path d="M250 300 Q320 330 380 300" />
      </g>
      <g fill="#ccfbf1">
        <circle cx="245" cy="165" r="6" />
        <circle cx="470" cy="225" r="6" />
        <circle cx="250" cy="300" r="6" />
      </g>
      {[
        [245, 110],
        [70, 230],
        [380, 230],
      ].map(([x, y]) => (
        <g key={`${x}-${y}`} transform={`translate(${x} ${y})`}>
          <rect width="190" height="105" rx="14" fill="url(#card)" stroke="#ffffff" strokeOpacity="0.7" />
          <path d="M22 18h22v30l-11-7-11 7z" fill="none" stroke="#0f766e" strokeWidth="3" strokeLinejoin="round" />
          <rect x="22" y="62" width="146" height="8" rx="4" fill="#ffffff" fillOpacity="0.85" />
          <rect x="22" y="78" width="104" height="8" rx="4" fill="#ffffff" fillOpacity="0.65" />
        </g>
      ))}
    </svg>
  )
}

const stroke = { fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round', strokeLinejoin: 'round' } as const

export function FolderIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" fill="currentColor" opacity="0.85" />
    </svg>
  )
}

export function ChevronIcon({ open }: { open: boolean }) {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" aria-hidden="true" focusable="false" className={open ? 'chev open' : 'chev'}>
      <path d="M9 5l7 7-7 7" {...stroke} />
    </svg>
  )
}

export function StarIcon({ filled }: { filled: boolean }) {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M12 3.5l2.6 5.4 5.9.8-4.3 4.1 1 5.9L12 16.9 6.8 19.7l1-5.9L3.5 9.7l5.9-.8z" {...stroke} fill={filled ? 'currentColor' : 'none'} />
    </svg>
  )
}

export function ListIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M4 6h16M4 12h16M4 18h16" {...stroke} />
    </svg>
  )
}

export function GridIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z" {...stroke} />
    </svg>
  )
}
