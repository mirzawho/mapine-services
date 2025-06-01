"use strict";
const { server } = require("../shared");
const express = require("express");

/**
 * @typedef {import('moleculer').ServiceSchema} ServiceSchema Moleculer's Service Schema
 * @typedef {import('moleculer').Context} Context Moleculer's Context
 */

/** @type {ServiceSchema} */
module.exports = {
	name: "http",

	/**
	 * Settings
	 */
	settings: {
		port: process.env.PORT ?? 3000,
	},

	/**
	 * Methods
	 */
	methods: {
		async authenticateUser({ username, password }) {
			return this.broker.call("api.v1.auth.authenticate", {
				username,
				password,
			});
		},
		async authenticateApi({ token }) {
			return this.broker.call("api.v1.token.authenticate", { token });
		},
		listActions() {
			return this.broker.registry.actions.list({
				onlyAvailable: true,
				skipInternal: true,
				withEndpoints: true,
			});
		},
		findAction(name) {
			try {
				const listActions = this.listActions();

				const foundAction = listActions.find(
					(item) => item.name === name
				);

				return this.formatAction(foundAction ?? foundAction.action);
			} catch (error) {
				return undefined;
			}
		},
		formatAction(action) {
			let cache = false;

			if (typeof action.action.cache == "boolean") {
				cache = action.action.cache;
			}

			if (typeof action.action.cache == "object") {
				if (typeof action.action.cache.enabled == "boolean") {
					cache = action.action.cache.enabled;
				} else {
					cache = true;
				}
			}

			return {
				action: action.name,
				service: action.name.replace(`.${action.action.rawName}`, ""),
				auth: action.action.auth ?? false,
				cache: cache,
				visibility: action.action.visibility ?? "private",
				nodes: action.endpoints.map((item) => item.nodeID),
				params: action.action.params,
			};
		},
		start() {
			this.listener = server.listen(this.settings.port, () => {
				this.logger.info(`http://localhost:${this.settings.port}`);
			});

			server.use('/public', express.static('./public'));

			server.all("/v1/call/:action", async (req, res) => {
				// concat queries and body
				const params = {
					...req.query,
					...req.body,
				};

				const action = req.params.action;

				req.meta.action = action;
				req.meta.params = params;

				if (action.startsWith("$")) {
					return res.status(406).json({
						ok: false,
						message: "BAD_ACTION",
					});
				}

				const foundAction = this.findAction(action);

				if (!foundAction || foundAction.visibility !== "published") {
					return res.status(404).json({
						ok: false,
						message: "ACTION_NOT_FOUND",
					});
				}

				const { auth, api } = foundAction;

				// check auth
				if (auth) {
					const accessToken = req.meta.accessToken;

					if (!accessToken) {
						return res.status(403).json({
							ok: false,
							message: "FORBIDDEN",
							error: "NEED_TOKEN",
						});
					}

					if (accessToken) {
						try {
							const accessData = await this.authenticateUser({
								username: accessToken.username,
								password: accessToken.password,
							});

							if (
								!accessData ||
								Object.keys(accessData).length === 0
							) {
								return res.status(403).json({
									ok: false,
									message: "FORBIDDEN",
									error: "BAD_USER",
								});
							}

							req.meta.user = accessData;
						} catch (error) {
							console.error(error);

							return res.status(403).json({
								ok: false,
								message: "FORBIDDEN",
								error: "BAD_TOKEN",
							});
						}
					}
				}

				try {
					const result = await this.broker.call(action, params, {
						meta: req.meta,
					});

					if (result.code === 301) {
						return res.status(301).redirect(result.url);
					}

					if (result.code === 500) {
						return res.status(500).json({
							ok: false,
							message: "INTERNAL_SERVER_ERROR",
							error: "SERVICE_ERROR",
						});
					}

					if (result.code === 502) {
						return res.status(500).json({
							ok: false,
							message: "BAD_GATEWAY",
						});
					}

					res
						.status(result.code ?? 200)
						.json(result);
				} catch (error) {
					if (error.message === "Parameters validation error!") {
						return res.status(400).json({
							ok: false,
							message: "VALIDATION_FAILED",
							error: "BAD_PARAMS",
							params: error.data.map((item) => ({
								message: item.message,
								param: item.field,
								type: item.type,
							})),
						});
					}
				}
			});

			server.get("/", (req, res) => {
				res.send(`Welcome to ${this.broker.namespace}`);
			});

			server.use("*", (req, res) => {
				res.status(404).end();
			});
		},
	},

	/**
	 * Service created lifecycle event handler
	 */
	created() { },

	/**
	 * Service started lifecycle event handler
	 */
	async started() {
		this.start();
	},

	/**
	 * Service stopped lifecycle event handler
	 */
	async stopped() {
		if (this.listener) {
			this.listener.close();
		}
	},
};
