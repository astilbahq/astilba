import { useInfiniteQuery } from "@tanstack/react-query";
import { useEffect, useRef, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import type { Schema } from "../../api/client";
import { Button } from "../../components/button";
import * as s from "../../styles";
import { secretAccessOptions } from "./secret-access-queries";
import { useSecretAccessText } from "./secret-access-text";
import * as styles from "./secret-access.styles";

// Owners only: the API refuses everyone else.
export function SecretAccess({ workspaceId }: { workspaceId: string }) {
  const { t } = useTranslation("secret-access");
  const access = useInfiniteQuery(secretAccessOptions(workspaceId));
  const heading = useRef<HTMLHeadingElement>(null);
  const region = useRef<HTMLElement>(null);
  const records = access.data?.pages.flatMap((page) => page.records);
  const count = records?.length ?? 0;
  // The index of the first record a Load more will add. Once the last page is
  // in, the button goes, so focus moves to that record instead of being lost.
  const firstNew = useRef<number | null>(null);
  const loadingMore = access.isFetchingNextPage;
  const loadMoreFailed = access.isFetchNextPageError;
  const more = access.hasNextPage;
  useEffect(() => {
    const index = firstNew.current;
    if (index === null || loadingMore) return;
    if (loadMoreFailed) {
      firstNew.current = null;
      return;
    }
    if (count <= index) return;
    firstNew.current = null;
    if (!more)
      region.current?.querySelectorAll<HTMLElement>("tbody th")[index]?.focus();
  }, [count, loadingMore, loadMoreFailed, more]);
  return (
    <section className={s.panel} aria-label={t(($) => $.title)}>
      <div className={styles.intro}>
        <h2 ref={heading} tabIndex={-1} className={styles.focusTarget}>
          {t(($) => $.title)}
        </h2>
        <p className={s.muted}>{t(($) => $.description)}</p>
        <p className={s.muted}>{t(($) => $.retention)}</p>
      </div>
      {access.isPending ? (
        <p role="status">{t(($) => $.loading)}</p>
      ) : !records ? (
        <div className={styles.part} role="alert">
          <p>{t(($) => $.loadFailed)}</p>
          <div>
            <Button
              onClick={() => {
                heading.current?.focus();
                void access.refetch();
              }}
            >
              {t(($) => $.retry)}
            </Button>
          </div>
        </div>
      ) : records.length === 0 ? (
        <p>{t(($) => $.empty)}</p>
      ) : (
        <>
          <AccessTable records={records} region={region} />
          <div className={styles.more}>
            {/* Present from the start so that a change in count is announced. */}
            <p role="status" className={s.muted}>
              {access.hasNextPage || (access.data?.pages.length ?? 0) > 1
                ? t(($) => $.shown, { count: records.length })
                : ""}
            </p>
            {access.hasNextPage && (
              <Button
                // Keeps focus on the button while the next page loads.
                focusableWhenDisabled
                disabled={loadingMore}
                onClick={() => {
                  firstNew.current = records.length;
                  void access.fetchNextPage();
                }}
              >
                {loadingMore ? t(($) => $.loadingMore) : t(($) => $.loadMore)}
              </Button>
            )}
          </div>
          {/* Unmounted while retrying, so that each failure is announced. */}
          {loadMoreFailed && !loadingMore && (
            <p className={s.error} role="alert">
              {t(($) => $.loadMoreFailed)}
            </p>
          )}
        </>
      )}
    </section>
  );
}

function AccessTable({
  records,
  region,
}: {
  records: Schema["SecretAccessRecord"][];
  region: RefObject<HTMLElement | null>;
}) {
  const { t } = useTranslation("secret-access");
  const text = useSecretAccessText();
  return (
    <section
      ref={region}
      className={styles.table}
      // oxlint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- WebKit needs focus to keyboard-scroll a clipped table.
      tabIndex={0}
      aria-label={t(($) => $.region)}
    >
      <table>
        <caption className={s.screenReaderOnly}>{t(($) => $.caption)}</caption>
        <thead>
          <tr>
            <th scope="col">{t(($) => $.column.when)}</th>
            <th scope="col">{t(($) => $.column.purpose)}</th>
            <th scope="col">{t(($) => $.column.principal)}</th>
            <th scope="col">{t(($) => $.column.items)}</th>
            <th scope="col">{t(($) => $.column.reason)}</th>
            <th scope="col">{t(($) => $.column.outcome)}</th>
          </tr>
        </thead>
        <tbody>
          {records.map((record) => {
            const principal = text.principal(record);
            const more = record.itemCount - record.items.length;
            return (
              <tr key={record.id}>
                <th scope="row" tabIndex={-1} className={styles.nowrap}>
                  <time dateTime={record.at}>{text.date(record.at)}</time>
                </th>
                <td>{text.purpose(record.purpose)}</td>
                <td>
                  <bdi className={styles.primary}>{principal.name}</bdi>
                  {principal.detail && (
                    <bdi className={styles.metadata}>{principal.detail}</bdi>
                  )}
                </td>
                <td>
                  {record.items.length === 0 && more <= 0 ? (
                    text.noItems()
                  ) : (
                    <ul className={styles.items}>
                      {record.items.map((item) => (
                        <li key={`${item.kind}:${item.id ?? "all"}`}>
                          <bdi>{text.item(item)}</bdi>
                        </li>
                      ))}
                      {more > 0 && (
                        <li className={styles.metadata}>{text.more(more)}</li>
                      )}
                    </ul>
                  )}
                </td>
                <td>{text.reason(record.reason)}</td>
                <td className={styles.nowrap}>
                  {text.outcome(record.outcome)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}
