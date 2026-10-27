/**
 * Longest question the Ask feature accepts. Mirrors the backend cap in
 * services/intelligence/app/api/routes.py.
 *
 * Kept out of the route module on purpose: the dashboard's Ask card needs it
 * too, and a component must not import a route file (that would pull the whole
 * Ask page, and its router wiring, into the dashboard bundle).
 */
export const MAX_QUESTION_LEN = 4000
