/** The server's rules for the label of a client or upstream key. */

/** The server's limit on a key's label, in characters. */
export const MAX_LABEL_LENGTH = 64;

/**
 * The problem with a label as entered, as a translation key, or null. The
 * server trims labels and refuses one longer than the limit or holding a
 * line break or other control character.
 */
export const labelProblem = (label: string): string | null => {
  const trimmed = label.trim();
  if ([...trimmed].length > MAX_LABEL_LENGTH) return 'labels.error_long';
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/.test(trimmed)) return 'labels.error_control';
  return null;
};
