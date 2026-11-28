import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink } from 'react-router-dom';

import { ROUTES } from '@shared/constants';

import * as styles from './LegalPage.styles';

export type LegalDocument = 'privacy' | 'terms';

interface LegalSection {
  id: string;
  title: string;
  paragraphs: string[];
}

/** Date of the current version of both documents, shown as `legal.lastUpdated`. */
const LAST_UPDATED = '2026-11-28';

const REPOSITORY_URL = 'https://github.com/ygtdmr/ft_transcendence';

/**
 * Shared screen for the Privacy Policy and Terms of Service: top bar, document card and
 * footer. Both documents are public and reachable from every footer, so the page never
 * depends on the session.
 *
 * The copy lives in the locale files as `<document>.sections`; the page splits it into two
 * columns on desktop. The `contact` section ends with a link to the project repository.
 */
export function LegalPage({ document }: { document: LegalDocument }) {
  const { t } = useTranslation();
  const sections = t(`${document}.sections`, { returnObjects: true }) as LegalSection[];
  const half = Math.ceil(sections.length / 2);
  const columns = [sections.slice(0, half), sections.slice(half)];

  // A document always opens at its top, whatever the scroll position of the last screen.
  useEffect(() => {
    window.scrollTo(0, 0);
  }, [document]);

  return (
    <div className={styles.page}>
      <header className={styles.topBar}>
        <Link to={ROUTES.landing} aria-label={t('legal.home')} className={styles.wordmark}>
          {t('legal.wordmark')}
        </Link>
        <nav aria-label={t('legal.navigation')} className={styles.topLinks}>
          <Link to={`${ROUTES.landing}#how-to-play`} className={styles.topLink}>
            {t('legal.howToPlay')}
          </Link>
          <NavLink to={ROUTES.privacy} className={styles.topLink}>
            {t('legal.privacy')}
          </NavLink>
          <NavLink to={ROUTES.terms} className={styles.topLink}>
            {t('legal.terms')}
          </NavLink>
          <Link to={ROUTES.login} className={styles.logIn}>
            {t('legal.logIn')}
          </Link>
        </nav>
      </header>

      <main className={styles.main}>
        <article aria-labelledby="legal-title" className={styles.card}>
          <header className="flex flex-col gap-[26px]">
            <h1 id="legal-title" className={styles.title}>
              {t(`${document}.title`)}
            </h1>
            <p className={styles.lastUpdated}>
              <time dateTime={LAST_UPDATED}>{t('legal.lastUpdated')}</time>
            </p>
            <p className={styles.intro}>{t(`${document}.intro`)}</p>
          </header>

          <hr className={styles.divider} />

          <div className={styles.columns}>
            {columns.map((column, index) => (
              <div key={index} className={styles.column}>
                {column.map((section) => (
                  <section
                    key={section.id}
                    aria-labelledby={`legal-${section.id}`}
                    className={styles.section}
                  >
                    <h2 id={`legal-${section.id}`} className={styles.sectionTitle}>
                      {section.title}
                    </h2>
                    <div className={styles.sectionBody}>
                      {section.paragraphs.map((paragraph) => (
                        <p key={paragraph} className={styles.paragraph}>
                          {paragraph}
                        </p>
                      ))}
                      {section.id === 'contact' && (
                        <a
                          href={REPOSITORY_URL}
                          target="_blank"
                          rel="external noopener noreferrer"
                          aria-label={t('legal.repository')}
                          className={styles.inlineLink}
                        >
                          github.com/ygtdmr/ft_transcendence
                        </a>
                      )}
                    </div>
                  </section>
                ))}
              </div>
            ))}
          </div>
        </article>
      </main>

      <footer className={styles.footer}>
        <nav aria-label={t('legal.legalNavigation')} className={styles.footerLinks}>
          <NavLink to={ROUTES.privacy} className={styles.footerLink}>
            {t('legal.privacyPolicy')}
          </NavLink>
          <span aria-hidden="true" className="whitespace-pre">
            {'   •   '}
          </span>
          <NavLink to={ROUTES.terms} className={styles.footerLink}>
            {t('legal.termsOfService')}
          </NavLink>
        </nav>
        <p className={styles.footerNote}>{t('legal.footer')}</p>
      </footer>
    </div>
  );
}
